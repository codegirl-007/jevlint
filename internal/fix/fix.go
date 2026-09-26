package fix

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const (
	diagnosticLimit    = 64 * 1024
	fixWorkspacePrefix = "jevlint-fix-"
)

type Options struct {
	Root         string
	ConfigPath   string
	Command      []string
	Context      []string
	Exclude      []string
	Findings     []runner.Finding
	Progress     func(string)
	AgentMessage func(string)
	ToolActivity func(string)
}

type Proposal struct {
	Changes  []FileChange
	Messages []string
}

type FileChange struct {
	Path   string
	Before []byte
	After  []byte
}

func ValidateSyntax(
	extractor *parsing.Extractor,
	changes []FileChange,
) error {
	for _, change := range changes {
		if _, err := extractor.Extract(change.Path, change.After); err != nil {
			return fmt.Errorf("%s: %w", change.Path, err)
		}
	}
	return nil
}

func Generate(ctx context.Context, options Options) (Proposal, error) {
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return Proposal{}, fmt.Errorf("resolve project root: %w", err)
	}
	if len(options.Command) == 0 {
		return Proposal{}, fmt.Errorf("ACP agent command is required")
	}
	reportProgress(options.Progress, "preparing temporary workspace")
	workspace, err := os.MkdirTemp("", fixWorkspacePrefix+"*")
	if err != nil {
		return Proposal{}, fmt.Errorf("create ACP workspace: %w", err)
	}
	removeStaleFixWorkspaces(os.TempDir(), workspace)
	defer removeFixWorkspace(workspace)

	before, err := mirrorProject(root, workspace, options)
	if err != nil {
		return Proposal{}, err
	}
	writable := writablePaths(workspace, before)
	client := &acpClient{
		root:         workspace,
		writable:     writable,
		progress:     options.Progress,
		agentMessage: options.AgentMessage,
		toolActivity: options.ToolActivity,
	}
	mcpServers, err := sessionMCPServers(root, workspace, options.ConfigPath)
	if err != nil {
		return Proposal{}, err
	}
	reportProgress(options.Progress, "starting ACP agent")
	reportProgress(options.Progress, "waiting for ACP agent to propose edits")
	if err := runACP(
		ctx,
		workspace,
		options.Command,
		buildPrompt(options.Findings),
		client,
		mcpServers,
	); err != nil {
		return Proposal{}, err
	}
	reportProgress(options.Progress, "inspecting proposed changes")
	changes, err := changedFiles(workspace, before)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{
		Changes:  changes,
		Messages: client.collectedMessages(),
	}, nil
}

func reportProgress(progress func(string), message string) {
	if progress != nil {
		progress(message)
	}
}

func writablePaths(
	workspace string,
	before map[string][]byte,
) map[string]struct{} {
	writable := make(map[string]struct{}, len(before))
	for relative := range before {
		writable[filepath.Join(workspace, filepath.FromSlash(relative))] = struct{}{}
	}
	return writable
}

func changedFiles(
	workspace string,
	before map[string][]byte,
) ([]FileChange, error) {
	if err := rejectUnexpectedFiles(workspace, before); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(before))
	for path := range before {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	changes := make([]FileChange, 0)
	for _, relative := range paths {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("ACP agent removed %q", relative)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf(
				"ACP agent replaced %q with a non-regular file",
				relative,
			)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read ACP result %q: %w", relative, err)
		}
		if bytes.Equal(before[relative], after) {
			continue
		}
		changes = append(changes, FileChange{
			Path:   relative,
			Before: before[relative],
			After:  after,
		})
	}
	return changes, nil
}

func rejectUnexpectedFiles(
	workspace string,
	before map[string][]byte,
) error {
	return filepath.WalkDir(
		workspace,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == workspace || entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if _, ok := before[relative]; !ok {
				return fmt.Errorf("ACP agent created unexpected file %q", relative)
			}
			return nil
		},
	)
}

func sessionMCPServers(
	root string,
	workspace string,
	configPath string,
) ([]acp.McpServer, error) {
	workspaceConfig, err := workspaceConfigPath(root, workspace, configPath)
	if err != nil {
		return nil, err
	}
	server, err := checkMCPServer(workspace, workspaceConfig, root)
	if err != nil {
		return nil, err
	}
	return []acp.McpServer{server}, nil
}

func runACP(
	ctx context.Context,
	workspace string,
	command []string,
	prompt string,
	client *acpClient,
	mcpServers []acp.McpServer,
) error {
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Dir = workspace
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := process.StdinPipe()
	if err != nil {
		return fmt.Errorf("open ACP stdin: %w", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open ACP stdout: %w", err)
	}
	diagnostics := &limitedBuffer{limit: diagnosticLimit}
	process.Stderr = diagnostics
	if err := process.Start(); err != nil {
		return fmt.Errorf("start ACP agent: %w", err)
	}

	connection := acp.NewClientSideConnection(client, stdin, stdout)
	connection.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionErr := runACPSession(ctx, connection, workspace, prompt, mcpServers)
	_ = stdin.Close()
	stopACPProcess(process)
	if sessionErr != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", sessionErr, detail)
		}
		return sessionErr
	}
	return nil
}

func stopACPProcess(process *exec.Cmd) {
	if process == nil || process.Process == nil {
		return
	}
	_ = syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
	_ = process.Process.Kill()
	_ = process.Wait()
}

func removeFixWorkspace(workspace string) {
	_ = os.RemoveAll(workspace)
}

func removeStaleFixWorkspaces(root string, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	keep = filepath.Clean(keep)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), fixWorkspacePrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if filepath.Clean(path) == keep {
			continue
		}
		_ = os.RemoveAll(path)
	}
}

func runACPSession(
	ctx context.Context,
	connection *acp.ClientSideConnection,
	workspace string,
	prompt string,
	mcpServers []acp.McpServer,
) error {
	response, err := connection.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo: &acp.Implementation{
			Name:    "jevlint",
			Version: "dev",
		},
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("initialize ACP agent: %w", err)
	}
	if response.ProtocolVersion != acp.ProtocolVersionNumber {
		return fmt.Errorf(
			"ACP agent selected unsupported protocol version %d",
			response.ProtocolVersion,
		)
	}
	session, err := connection.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: mcpServers,
	})
	if err != nil {
		return fmt.Errorf("create ACP session: %w", err)
	}
	result, err := connection.Prompt(ctx, acp.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	})
	if err != nil {
		return fmt.Errorf("run ACP fix: %w", err)
	}
	if response.AgentCapabilities.SessionCapabilities.Close != nil {
		closeContext, cancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		_, _ = connection.CloseSession(closeContext, acp.CloseSessionRequest{
			SessionId: session.SessionId,
		})
		cancel()
	}
	if result.StopReason != acp.StopReasonEndTurn {
		return fmt.Errorf("ACP agent stopped with reason %q", result.StopReason)
	}
	return nil
}

func buildPrompt(findings []runner.Finding) string {
	var prompt strings.Builder
	prompt.WriteString(
		"Fix every Jevlint finding below by editing the existing files in this " +
			"safe project snapshot. You may inspect and edit existing project files. " +
			"Do not create, delete, or rename " +
			"files. Keep behavior unchanged except where required by the rules. Do " +
			"not run the jevlint CLI. After edits, call the jevlint_check tool. Do " +
			"not finish until jevlint_check reports no findings. If it still reports " +
			"findings, keep fixing those files and check again. Jevlint will also " +
			"perform a final validation after you stop.\n",
	)
	for _, finding := range findings {
		fmt.Fprintf(
			&prompt,
			"\nRule %s: %s\nFile: %s:%d-%d\nTarget: %s %s\n",
			finding.RuleID,
			finding.Description,
			finding.Path,
			finding.StartLine,
			finding.EndLine,
			finding.Kind,
			finding.Name,
		)
		for _, location := range finding.Locations {
			fmt.Fprintf(
				&prompt,
				"Problem: %s:%d-%d\n",
				location.Kind,
				location.StartLine,
				location.EndLine,
			)
		}
	}
	return prompt.String()
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(content []byte) (int, error) {
	originalLength := len(content)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(content[:min(remaining, len(content))])
	}
	return originalLength, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}

var _ io.Writer = (*limitedBuffer)(nil)
