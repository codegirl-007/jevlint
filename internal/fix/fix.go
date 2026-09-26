package fix

import (
	"bytes"
	"context"
	"errors"
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
	diagnosticLimit  = 64 * 1024
	acpClientName    = "jevlint"
	acpClientVersion = "dev"
	acpCloseTimeout  = time.Second
)

type AgentFeedback struct {
	Progress func(string)
	Message  func(string)
	Tool     func(string)
}

type PathFilter struct {
	Context []string
	Exclude []string
}

type Workspace struct {
	Root       string
	ConfigPath string
	Paths      PathFilter
}

type AgentSession struct {
	Command  []string
	Feedback AgentFeedback
}

type Options struct {
	Workspace Workspace
	Agent     AgentSession
	Findings  []runner.Finding
}

type Proposal struct {
	Changes []FileChange
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
	root, err := filepath.Abs(options.Workspace.Root)
	if err != nil {
		return Proposal{}, fmt.Errorf("resolve project root: %w", err)
	}
	if len(options.Agent.Command) == 0 {
		return Proposal{}, fmt.Errorf("ACP agent command is required")
	}
	if options.Agent.Feedback.Progress != nil {
		options.Agent.Feedback.Progress("recording project snapshot")
	}
	snapshot, err := snapshotProject(root, options)
	if err != nil {
		return Proposal{}, err
	}
	writable := writablePaths(root, snapshot)
	client := &acpClient{
		root:         root,
		writable:     writable,
		progress:     options.Agent.Feedback.Progress,
		agentMessage: options.Agent.Feedback.Message,
		toolActivity: options.Agent.Feedback.Tool,
	}
	mcpServers, err := sessionMCPServers(root, options.Workspace.ConfigPath)
	if err != nil {
		return Proposal{}, err
	}
	if options.Agent.Feedback.Progress != nil {
		options.Agent.Feedback.Progress("starting ACP agent")
		options.Agent.Feedback.Progress("waiting for ACP agent to propose edits")
	}
	if err := runACP(
		ctx,
		root,
		options.Agent.Command,
		buildPrompt(options.Findings),
		client,
		mcpServers,
	); err != nil {
		return Proposal{}, restoreAfterFailure(root, snapshot, err)
	}
	if options.Agent.Feedback.Progress != nil {
		options.Agent.Feedback.Progress("inspecting proposed changes")
	}
	changes, err := changedFiles(root, snapshot)
	if err != nil {
		return Proposal{}, restoreAfterFailure(root, snapshot, err)
	}
	return Proposal{Changes: changes}, nil
}

func restoreAfterFailure(root string, snapshot projectSnapshot, err error) error {
	if restoreErr := removeUnknownProjectFiles(root, snapshot); restoreErr != nil {
		return fmt.Errorf("%w (restore failed: %v)", err, restoreErr)
	}
	if restoreErr := restoreSnapshotFiles(root, snapshot); restoreErr != nil {
		return fmt.Errorf("%w (restore failed: %v)", err, restoreErr)
	}
	return err
}

func writablePaths(
	root string,
	snapshot projectSnapshot,
) map[string]struct{} {
	writable := make(map[string]struct{}, len(snapshot.paths))
	for relative, entry := range snapshot.paths {
		if entry.captured {
			writable[filepath.Join(root, filepath.FromSlash(relative))] = struct{}{}
		}
	}
	return writable
}

func changedFiles(
	root string,
	snapshot projectSnapshot,
) ([]FileChange, error) {
	if err := rejectUnexpectedFiles(root, snapshot); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(snapshot.paths))
	for path, entry := range snapshot.paths {
		if entry.captured {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	changes := make([]FileChange, 0)
	for _, relative := range paths {
		change, changed, err := snapshotFileChange(root, relative, snapshot.paths[relative].file)
		if err != nil {
			return nil, err
		}
		if changed {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func snapshotFileChange(
	root string,
	relative string,
	before snapshotFile,
) (FileChange, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		return FileChange{}, false, fmt.Errorf("ACP agent removed %q", relative)
	}
	if !info.Mode().IsRegular() {
		return FileChange{}, false, fmt.Errorf(
			"ACP agent replaced %q with a non-regular file",
			relative,
		)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return FileChange{}, false, fmt.Errorf("read ACP result %q: %w", relative, err)
	}
	if bytes.Equal(before.content, after) {
		return FileChange{}, false, nil
	}
	return FileChange{
		Path:   relative,
		Before: before.content,
		After:  after,
	}, true, nil
}

func rejectUnexpectedFiles(root string, snapshot projectSnapshot) error {
	current, err := discoverProjectPaths(root, nil, snapshot.exclude)
	if err != nil {
		return err
	}
	for _, relative := range current {
		relative = filepath.ToSlash(relative)
		if _, ok := snapshot.paths[relative]; !ok {
			return fmt.Errorf("ACP agent created unexpected file %q", relative)
		}
	}
	return nil
}

func sessionMCPServers(
	root string,
	configPath string,
) ([]acp.McpServer, error) {
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, absoluteConfig)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, fmt.Errorf("config path must be inside project root")
	}
	server, err := checkMCPServer(root, absoluteConfig, root)
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
	process, stdin, stdout, diagnostics, err := startACPProcess(ctx, workspace, command)
	if err != nil {
		return err
	}

	connection := acp.NewClientSideConnection(client, stdin, stdout)
	connection.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	sessionErr := runACPSession(ctx, connection, workspace, prompt, mcpServers)
	closeErr := stdin.Close()
	stopErr := stopACPProcess(process)
	if sessionErr != nil {
		detail := strings.TrimSpace(diagnostics.buffer.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", sessionErr, detail)
		}
		return sessionErr
	}
	return errors.Join(closeErr, stopErr)
}

func startACPProcess(
	ctx context.Context,
	workspace string,
	command []string,
) (*exec.Cmd, io.WriteCloser, io.ReadCloser, *limitedBuffer, error) {
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Dir = workspace
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open ACP stdin: %w", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open ACP stdout: %w", err)
	}
	diagnostics := &limitedBuffer{limit: diagnosticLimit}
	process.Stderr = diagnostics
	if err := process.Start(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("start ACP agent: %w", err)
	}
	return process, stdin, stdout, diagnostics, nil
}

func stopACPProcess(process *exec.Cmd) error {
	if process == nil || process.Process == nil {
		return nil
	}
	var errs []error
	if err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL); err != nil && !processAlreadyGone(err) {
		errs = append(errs, err)
	}
	if err := process.Process.Kill(); err != nil && !processAlreadyGone(err) {
		errs = append(errs, err)
	}
	if err := process.Wait(); err != nil && !expectedKillWait(err) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func processAlreadyGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func expectedKillWait(err error) bool {
	if processAlreadyGone(err) {
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
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
			Name:    acpClientName,
			Version: acpClientVersion,
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
			acpCloseTimeout,
		)
		_, closeErr := connection.CloseSession(closeContext, acp.CloseSessionRequest{
			SessionId: session.SessionId,
		})
		cancel()
		if closeErr != nil && !errors.Is(closeErr, context.DeadlineExceeded) {
			return fmt.Errorf("close ACP session: %w", closeErr)
		}
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
			"project. You may inspect and edit existing project files. " +
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
		if _, err := buffer.buffer.Write(content[:min(remaining, len(content))]); err != nil {
			return 0, err
		}
	}
	return originalLength, nil
}

var _ io.Writer = (*limitedBuffer)(nil)
