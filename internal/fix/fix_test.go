package fix

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

func TestGenerateEditsProjectFiles(t *testing.T) {
	root := writeFixProject(t)
	t.Setenv("JEVLINT_ACP_HELPER", "success")
	var progressMu sync.Mutex
	var progress []string
	var agentText strings.Builder

	proposal, err := Generate(context.Background(), Options{
		Root:       root,
		ConfigPath: filepath.Join(root, "jevlint.json"),
		Command:    []string{os.Args[0], "-test.run=TestACPHelperProcess"},
		Findings:   []runner.Finding{testFinding()},
		Progress: func(message string) {
			progressMu.Lock()
			progress = append(progress, message)
			progressMu.Unlock()
		},
		AgentMessage: func(message string) {
			progressMu.Lock()
			agentText.WriteString(message)
			progressMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(proposal.Changes) != 1 ||
		proposal.Changes[0].Path != "sample.go" ||
		!strings.Contains(string(proposal.Changes[0].After), "Good") {
		t.Fatalf("Generate() proposal = %#v", proposal)
	}
	original, err := os.ReadFile(filepath.Join(root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), "Good") {
		t.Fatalf("Generate() left project source unchanged: %q", original)
	}
	progressMu.Lock()
	progressText := strings.Join(progress, "\n")
	messageText := agentText.String()
	progressMu.Unlock()
	for _, expected := range []string{
		"recording project snapshot",
		"starting ACP agent",
		"waiting for ACP agent to propose edits",
		"inspecting proposed changes",
	} {
		if !strings.Contains(progressText, expected) {
			t.Fatalf("progress = %q, want %q", progressText, expected)
		}
	}
	if messageText != "fixed" {
		t.Fatalf("agent messages = %q, want %q", messageText, "fixed")
	}
}

func TestGenerateRejectsUnexpectedFiles(t *testing.T) {
	root := writeFixProject(t)
	t.Setenv("JEVLINT_ACP_HELPER", "unexpected")

	_, err := Generate(context.Background(), Options{
		Root:       root,
		ConfigPath: filepath.Join(root, "jevlint.json"),
		Command:    []string{os.Args[0], "-test.run=TestACPHelperProcess"},
		Findings:   []runner.Finding{testFinding()},
	})
	if err == nil || !strings.Contains(err.Error(), "created unexpected file") {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "extra.go")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected file was not restored away: %v", statErr)
	}
}

func TestGenerateAcceptsEditsToExistingProjectFiles(t *testing.T) {
	root := writeFixProject(t)
	t.Setenv("JEVLINT_ACP_HELPER", "modify-context")

	proposal, err := Generate(context.Background(), Options{
		Root:       root,
		ConfigPath: filepath.Join(root, "jevlint.json"),
		Command:    []string{os.Args[0], "-test.run=TestACPHelperProcess"},
		Findings:   []runner.Finding{testFinding()},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(proposal.Changes) != 1 || proposal.Changes[0].Path != "jevlint.json" {
		t.Fatalf("Generate() proposal = %#v", proposal)
	}
}

func TestGenerateRejectsUnsafeWorkspaceChanges(t *testing.T) {
	tests := map[string]string{
		"remove-context": "removed",
		"replace-target": "replaced",
	}
	for mode, expected := range tests {
		mode, expected := mode, expected
		t.Run(mode, func(t *testing.T) {
			t.Setenv("JEVLINT_ACP_HELPER", mode)
			root := writeFixProject(t)

			_, err := Generate(context.Background(), Options{
				Root:       root,
				ConfigPath: filepath.Join(root, "jevlint.json"),
				Command: []string{
					os.Args[0],
					"-test.run=TestACPHelperProcess",
				},
				Findings: []runner.Finding{testFinding()},
			})
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("Generate() error = %v, want %q", err, expected)
			}
			content, readErr := os.ReadFile(filepath.Join(root, "sample.go"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(content), "Bad") {
				t.Fatalf("rejected Generate() did not restore sample.go: %q", content)
			}
		})
	}
}

func TestGenerateCancelsACPAgent(t *testing.T) {
	root := writeFixProject(t)
	t.Setenv("JEVLINT_ACP_HELPER", "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Generate(ctx, Options{
		Root:       root,
		ConfigPath: filepath.Join(root, "jevlint.json"),
		Command:    []string{os.Args[0], "-test.run=TestACPHelperProcess"},
		Findings:   []runner.Finding{testFinding()},
	})
	if err == nil {
		t.Fatal("Generate() cancellation error = nil")
	}
}

func TestValidateSyntaxRejectsInvalidChange(t *testing.T) {
	extractor, err := parsing.NewExtractor(map[string]config.Language{"go": {}})
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateSyntax(extractor, []FileChange{{
		Path:  "sample.go",
		After: []byte("package sample\nfunc {"),
	}})
	if err == nil || !strings.Contains(err.Error(), "source contains syntax errors") {
		t.Fatalf("ValidateSyntax() error = %v", err)
	}
}

func TestBuildPromptDescribesSnapshotAndTargetedChecks(t *testing.T) {
	t.Parallel()

	prompt := buildPrompt([]runner.Finding{testFinding()})
	for _, expected := range []string{
		"editing the existing files in this project",
		"inspect and edit existing project files",
		"Do not run the jevlint CLI",
		"call the jevlint_check tool",
		"Do not finish until jevlint_check reports no findings",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("buildPrompt() = %q, want %q", prompt, expected)
		}
	}
}

func TestSessionMCPServersUsesProjectRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "jevlint.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	servers, err := sessionMCPServers(root, filepath.Join(root, "jevlint.json"))
	if err != nil {
		t.Fatalf("sessionMCPServers() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Stdio == nil || servers[0].Stdio.Args[0] != "mcp-check" {
		t.Fatalf("sessionMCPServers() = %#v", servers)
	}
	var cacheRoot string
	for _, env := range servers[0].Stdio.Env {
		if env.Name == "JEVLINT_MCP_CACHE_ROOT" {
			cacheRoot = env.Value
		}
	}
	if cacheRoot != root {
		t.Fatalf("JEVLINT_MCP_CACHE_ROOT = %q, want %q", cacheRoot, root)
	}
}

func TestPermissionAllowedRequiresContainedLocations(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "sample.go")
	writable := map[string]struct{}{target: {}}
	edit := acp.ToolKindEdit

	if !permissionAllowed(root, writable, acp.ToolCallUpdate{
		Kind:      &edit,
		Locations: []acp.ToolCallLocation{{Path: target}},
	}) {
		t.Fatal("permissionAllowed() = false for contained edit")
	}
	if permissionAllowed(root, writable, acp.ToolCallUpdate{
		Kind:      &edit,
		Locations: []acp.ToolCallLocation{{Path: filepath.Join(root, "..", "outside.go")}},
	}) {
		t.Fatal("permissionAllowed() = true for escaping edit")
	}
}

func TestACPClientRejectsSymlinkRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := (&acpClient{root: root}).ReadTextFile(
		context.Background(),
		acp.ReadTextFileRequest{Path: link},
	)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("ReadTextFile() error = %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(outside, "secret.txt"),
		[]byte("secret"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(root, "escaped")
	if err := os.Symlink(outside, escaped); err != nil {
		t.Fatal(err)
	}
	_, err = (&acpClient{root: root}).ReadTextFile(
		context.Background(),
		acp.ReadTextFileRequest{Path: filepath.Join(escaped, "secret.txt")},
	)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("ReadTextFile() parent symlink error = %v", err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	diff, err := UnifiedDiff([]FileChange{{
		Path:   "sample.go",
		Before: []byte("package sample\n\nvar Bad = true\n"),
		After:  []byte("package sample\n\nvar Good = true\n"),
	}})
	if err != nil {
		t.Fatalf("UnifiedDiff() error = %v", err)
	}
	for _, expected := range []string{
		"--- a/sample.go",
		"+++ b/sample.go",
		"-var Bad = true",
		"+var Good = true",
	} {
		if !strings.Contains(diff, expected) {
			t.Fatalf("UnifiedDiff() = %q, want %q", diff, expected)
		}
	}
}

func TestACPHelperProcess(t *testing.T) {
	mode := os.Getenv("JEVLINT_ACP_HELPER")
	if mode == "" {
		return
	}
	agent := &fakeACPAgent{mode: mode}
	connection := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.connection = connection
	<-connection.Done()
}

type fakeACPAgent struct {
	mode       string
	cwd        string
	connection *acp.AgentSideConnection
}

func (agent *fakeACPAgent) Initialize(
	context.Context,
	acp.InitializeRequest,
) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{},
		AuthMethods:       []acp.AuthMethod{},
	}, nil
}

func (agent *fakeACPAgent) NewSession(
	_ context.Context,
	request acp.NewSessionRequest,
) (acp.NewSessionResponse, error) {
	agent.cwd = request.Cwd
	return acp.NewSessionResponse{SessionId: "test-session"}, nil
}

func (agent *fakeACPAgent) Prompt(
	ctx context.Context,
	request acp.PromptRequest,
) (acp.PromptResponse, error) {
	path := filepath.Join(agent.cwd, "sample.go")
	switch agent.mode {
	case "success":
		content, err := os.ReadFile(path)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		content = []byte(strings.ReplaceAll(string(content), "Bad", "Good"))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return acp.PromptResponse{}, err
		}
	case "unexpected":
		if err := os.WriteFile(
			filepath.Join(agent.cwd, "extra.go"),
			[]byte("package sample\n"),
			0o600,
		); err != nil {
			return acp.PromptResponse{}, err
		}
	case "modify-context":
		if err := os.WriteFile(
			filepath.Join(agent.cwd, "jevlint.json"),
			[]byte("{}\n"),
			0o600,
		); err != nil {
			return acp.PromptResponse{}, err
		}
	case "remove-context":
		if err := os.Remove(filepath.Join(agent.cwd, "jevlint.json")); err != nil {
			return acp.PromptResponse{}, err
		}
	case "replace-target":
		if err := os.Remove(path); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := os.Symlink(filepath.Join(agent.cwd, "jevlint.json"), path); err != nil {
			return acp.PromptResponse{}, err
		}
	case "hang":
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	_ = agent.connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: request.SessionId,
		Update:    acp.UpdateAgentMessageText("fixed"),
	})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (*fakeACPAgent) Authenticate(
	context.Context,
	acp.AuthenticateRequest,
) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (*fakeACPAgent) Logout(
	context.Context,
	acp.LogoutRequest,
) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (*fakeACPAgent) Cancel(context.Context, acp.CancelNotification) error {
	return nil
}

func (*fakeACPAgent) CloseSession(
	context.Context,
	acp.CloseSessionRequest,
) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (*fakeACPAgent) ListSessions(
	context.Context,
	acp.ListSessionsRequest,
) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (*fakeACPAgent) ResumeSession(
	context.Context,
	acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (*fakeACPAgent) SetSessionConfigOption(
	context.Context,
	acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (*fakeACPAgent) SetSessionMode(
	context.Context,
	acp.SetSessionModeRequest,
) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func writeFixProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "sample.go"),
		[]byte("package sample\n\nvar Bad = true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "jevlint.json"),
		[]byte(`{"languages":{"go":{}},"rules":[]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return root
}

func testFinding() runner.Finding {
	return runner.Finding{
		RuleID:      "example",
		Description: "Do not use Bad.",
		Severity:    config.SeverityError,
		Status:      evaluation.StatusFail,
		Path:        "sample.go",
		Language:    "go",
		Kind:        parsing.CodeKindStatement,
		Name:        "Bad",
		StartLine:   3,
		EndLine:     3,
		Snippet:     "var Bad = true",
	}
}
