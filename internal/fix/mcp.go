package fix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	acp "github.com/coder/acp-go-sdk"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const checkToolName = "jevlint_check"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func checkMCPServer(workspace string, configPath string, cacheRoot string) (acp.McpServer, error) {
	executable, err := os.Executable()
	if err != nil {
		return acp.McpServer{}, fmt.Errorf("resolve jevlint executable: %w", err)
	}
	return acp.McpServer{
		Stdio: &acp.McpServerStdio{
			Name:    "jevlint",
			Command: executable,
			Args:    []string{"mcp-check"},
			Env:     checkMCPEnv(workspace, configPath, cacheRoot),
		},
	}, nil
}

func checkMCPEnv(workspace string, configPath string, cacheRoot string) []acp.EnvVariable {
	env := []acp.EnvVariable{
		{Name: "JEVLINT_MCP_ROOT", Value: workspace},
		{Name: "JEVLINT_MCP_CONFIG", Value: configPath},
		{Name: "JEVLINT_MCP_CACHE_ROOT", Value: cacheRoot},
	}
	for _, name := range []string{
		"TYPESAFE_API_KEY",
		"TYPESAFE_BASE_URL",
		"TYPESAFE_DEFAULT_MODEL",
		"XDG_CACHE_HOME",
		"HOME",
		"SSL_CERT_FILE",
		"SSL_CERT_DIR",
		"HTTPS_PROXY",
		"HTTP_PROXY",
		"NO_PROXY",
	} {
		if value := os.Getenv(name); value != "" {
			env = append(env, acp.EnvVariable{Name: name, Value: value})
		}
	}
	return env
}

func ServeCheck(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(stdin)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := readMCPMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var request mcpRequest
		if err := json.Unmarshal(message, &request); err != nil {
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if request.Method == "" || len(request.ID) == 0 {
			continue
		}
		response, err := handleMCPRequest(ctx, request)
		if err != nil {
			return err
		}
		if err := writeMCPMessage(stdout, response); err != nil {
			return err
		}
	}
}

func handleMCPRequest(ctx context.Context, request mcpRequest) ([]byte, error) {
	switch request.Method {
	case "initialize":
		return mcpResult(request.ID, map[string]any{
			"protocolVersion": initializeProtocolVersion(request.Params),
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "jevlint",
				"version": "dev",
			},
		})
	case "ping":
		return mcpResult(request.ID, map[string]any{})
	case "tools/list":
		return mcpResult(request.ID, map[string]any{
			"tools": []map[string]any{{
				"name": checkToolName,
				"description": "Run Jevlint check on the current workspace. " +
					"Does not apply fixes. Uses the project evaluation cache. " +
					"You must call this and get no findings before finishing an autofix.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			}},
		})
	case "tools/call":
		var call mcpToolCall
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return mcpError(request.ID, "invalid tools/call params")
		}
		if call.Name != checkToolName {
			return mcpError(request.ID, "unknown tool "+call.Name)
		}
		text, err := runWorkspaceCheck(ctx)
		if err != nil {
			return mcpResult(request.ID, map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": "jevlint check failed: " + err.Error(),
				}},
				"isError": true,
			})
		}
		return mcpResult(request.ID, map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": text,
			}},
		})
	default:
		return mcpError(request.ID, "method not found: "+request.Method)
	}
}

func runWorkspaceCheck(ctx context.Context) (string, error) {
	root := os.Getenv("JEVLINT_MCP_ROOT")
	configPath := os.Getenv("JEVLINT_MCP_CONFIG")
	if root == "" || configPath == "" {
		return "", fmt.Errorf("JEVLINT_MCP_ROOT and JEVLINT_MCP_CONFIG are required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	extractor, err := parsing.NewExtractor(cfg.Languages)
	if err != nil {
		return "", err
	}
	cacheRoot := os.Getenv("JEVLINT_MCP_CACHE_ROOT")
	if cacheRoot == "" {
		cacheRoot = root
	}
	resultCache, err := evaluation.NewFileCache(cacheRoot)
	if err != nil {
		return "", fmt.Errorf("evaluation cache: %w", err)
	}
	evaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{Cache: resultCache},
	)
	if err != nil {
		return "", err
	}
	report, err := (runner.Runner{
		Extractor: extractor,
		Evaluator: evaluator,
	}).Check(ctx, cfg, runner.Options{
		Root:        root,
		Paths:       []string{"."},
		Concurrency: 4,
	})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"findings":     report.Findings,
		"scannedFiles": report.ScannedFiles,
		"codeUnits":    report.CodeUnits,
		"evaluations":  report.Evaluations,
		"ok":           !report.HasFailures(),
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func mcpResult(id json.RawMessage, result any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRaw(id),
		"result":  result,
	})
}

func mcpError(id json.RawMessage, message string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRaw(id),
		"error": map[string]any{
			"code":    -32601,
			"message": message,
		},
	})
}

func jsonRaw(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(id, &value); err != nil {
		return nil
	}
	return value
}

func initializeProtocolVersion(params json.RawMessage) string {
	const fallback = "2024-11-05"
	var decoded struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fallback
	}
	switch decoded.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return decoded.ProtocolVersion
	default:
		return fallback
	}
}

func readMCPMessage(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					return line, nil
				}
			}
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		return line, nil
	}
}

func writeMCPMessage(writer io.Writer, payload []byte) error {
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if _, err := writer.Write([]byte{'\n'}); err != nil {
		return err
	}
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func workspaceConfigPath(root string, workspace string, configPath string) (string, error) {
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absoluteConfig)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("config path must be inside project root")
	}
	return filepath.Join(workspace, relative), nil
}
