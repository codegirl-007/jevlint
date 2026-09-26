package fix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

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
			Env:     checkMCPEnv(workspace, configPath, cacheRoot, os.Getenv),
		},
	}, nil
}

func checkMCPEnv(
	workspace string,
	configPath string,
	cacheRoot string,
	getenv func(string) string,
) []acp.EnvVariable {
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
		if value := getenv(name); value != "" {
			env = append(env, acp.EnvVariable{Name: name, Value: value})
		}
	}
	return env
}

func ServeMCP(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	getenv func(string) string,
) error {
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
		response, err := handleMCPRequest(ctx, request, getenv)
		if err != nil {
			return err
		}
		if err := writeMCPMessage(stdout, response); err != nil {
			return err
		}
	}
}

func handleMCPRequest(
	ctx context.Context,
	request mcpRequest,
	getenv func(string) string,
) ([]byte, error) {
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
		text, err := runWorkspaceCheck(ctx, getenv)
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

func runWorkspaceCheck(ctx context.Context, getenv func(string) string) (string, error) {
	root := getenv("JEVLINT_MCP_ROOT")
	configPath := getenv("JEVLINT_MCP_CONFIG")
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
	cacheRoot := getenv("JEVLINT_MCP_CACHE_ROOT")
	if cacheRoot == "" {
		cacheRoot = root
	}
	resultCache, err := evaluation.NewFileCache(cacheRoot, os.UserCacheDir)
	if err != nil {
		return "", fmt.Errorf("evaluation cache: %w", err)
	}
	evaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{Cache: resultCache},
		getenv,
	)
	if err != nil {
		return "", err
	}
	report, err := (runner.Runner{
		Extractor: extractor,
		Evaluator: evaluator,
	}).Evaluate(ctx, cfg, runner.Options{
		Root: root,
		// jevlint_check always scans the project. --changed only scopes
		// the opening CLI check.
		Paths: []string{"."},
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
	message := map[string]any{
		"jsonrpc": "2.0",
		"result":  result,
	}
	if len(id) > 0 {
		copied := make(json.RawMessage, len(id))
		copy(copied, id)
		message["id"] = copied
	}
	return json.Marshal(message)
}

func mcpError(id json.RawMessage, message string) ([]byte, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    jsonRPCMethodNotFound,
			"message": message,
		},
	}
	if len(id) > 0 {
		copied := make(json.RawMessage, len(id))
		copy(copied, id)
		payload["id"] = copied
	}
	return json.Marshal(payload)
}

const (
	jsonRPCMethodNotFound = -32601
	mcpProtocol2024_11_05 = "2024-11-05"
	mcpProtocol2025_03_26 = "2025-03-26"
	mcpProtocol2025_06_18 = "2025-06-18"
)

func initializeProtocolVersion(params json.RawMessage) string {
	var decoded struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return mcpProtocol2024_11_05
	}
	switch decoded.ProtocolVersion {
	case mcpProtocol2024_11_05, mcpProtocol2025_03_26, mcpProtocol2025_06_18:
		return decoded.ProtocolVersion
	default:
		return mcpProtocol2024_11_05
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
