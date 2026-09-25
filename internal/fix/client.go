package fix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

type acpClient struct {
	root         string
	writable     map[string]struct{}
	progress     func(string)
	agentMessage func(string)
	toolActivity func(string)
	mu           sync.Mutex
	messages     []string
}

func (client *acpClient) ReadTextFile(
	_ context.Context,
	request acp.ReadTextFileRequest,
) (acp.ReadTextFileResponse, error) {
	path, err := client.containedPath(request.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	if err := requireRegularContainedFile(client.root, path); err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return acp.ReadTextFileResponse{
		Content: selectedLines(string(content), request.Line, request.Limit),
	}, nil
}

func (client *acpClient) WriteTextFile(
	_ context.Context,
	request acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	path, err := client.containedPath(request.Path)
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if _, ok := client.writable[path]; !ok {
		return acp.WriteTextFileResponse{}, fmt.Errorf(
			"ACP agent cannot modify %q",
			request.Path,
		)
	}
	if err := requireRegularContainedFile(client.root, path); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{}, os.WriteFile(
		path,
		[]byte(request.Content),
		0o600,
	)
}

func (client *acpClient) RequestPermission(
	_ context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	if permissionAllowed(client.root, client.writable, request.ToolCall) {
		if option, ok := permissionOption(
			request.Options,
			acp.PermissionOptionKindAllowOnce,
		); ok {
			return selectedPermission(option), nil
		}
	}
	if option, ok := permissionOption(
		request.Options,
		acp.PermissionOptionKindRejectOnce,
	); ok {
		return selectedPermission(option), nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeCancelled(),
	}, nil
}

func (client *acpClient) SessionUpdate(
	_ context.Context,
	notification acp.SessionNotification,
) error {
	update := notification.Update
	if update.ToolCall != nil && update.ToolCall.Title != "" {
		reportProgress(client.toolActivity, update.ToolCall.Title)
	}
	if update.ToolCallUpdate != nil && update.ToolCallUpdate.Title != nil {
		reportProgress(client.toolActivity, *update.ToolCallUpdate.Title)
	}
	if update.AgentMessageChunk == nil ||
		update.AgentMessageChunk.Content.Text == nil {
		return nil
	}
	rawText := update.AgentMessageChunk.Content.Text.Text
	if client.agentMessage != nil {
		client.agentMessage(rawText)
	}
	text := strings.TrimSpace(rawText)
	if text == "" {
		return nil
	}
	client.mu.Lock()
	client.messages = append(client.messages, text)
	client.mu.Unlock()
	return nil
}

func (*acpClient) CreateTerminal(
	context.Context,
	acp.CreateTerminalRequest,
) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errors.New("ACP terminal access is disabled")
}

func (*acpClient) KillTerminal(
	context.Context,
	acp.KillTerminalRequest,
) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("ACP terminal access is disabled")
}

func (*acpClient) TerminalOutput(
	context.Context,
	acp.TerminalOutputRequest,
) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("ACP terminal access is disabled")
}

func (*acpClient) ReleaseTerminal(
	context.Context,
	acp.ReleaseTerminalRequest,
) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("ACP terminal access is disabled")
}

func (*acpClient) WaitForTerminalExit(
	context.Context,
	acp.WaitForTerminalExitRequest,
) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("ACP terminal access is disabled")
}

func (client *acpClient) containedPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("ACP path must be absolute: %q", path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(client.root, path)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("ACP path escapes temporary workspace: %q", path)
	}
	return path, nil
}

func requireRegularContainedFile(root string, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("ACP path escapes temporary workspace: %q", path)
	}
	parts := strings.Split(filepath.Clean(relative), string(os.PathSeparator))
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if index == len(parts)-1 && info.Mode().IsRegular() {
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf(
				"ACP path contains a non-directory or non-regular component: %q",
				path,
			)
		}
	}
	return fmt.Errorf("ACP path is not a regular file: %q", path)
}

func (client *acpClient) collectedMessages() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string(nil), client.messages...)
}

func selectedLines(content string, line *int, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 0 {
		start = min(*line-1, len(lines))
	}
	end := len(lines)
	if limit != nil && *limit > 0 {
		end = min(start+*limit, end)
	}
	return strings.Join(lines[start:end], "\n")
}

func permissionAllowed(
	root string,
	writable map[string]struct{},
	call acp.ToolCallUpdate,
) bool {
	if call.Kind == nil {
		return false
	}
	switch *call.Kind {
	case acp.ToolKindRead, acp.ToolKindSearch:
	case acp.ToolKindEdit:
	default:
		return false
	}
	if len(call.Locations) == 0 {
		return false
	}
	for _, location := range call.Locations {
		if !filepath.IsAbs(location.Path) {
			return false
		}
		path := filepath.Clean(location.Path)
		relative, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(relative) {
			return false
		}
		if *call.Kind == acp.ToolKindEdit {
			if _, ok := writable[path]; !ok {
				return false
			}
		}
	}
	return true
}

func permissionOption(
	options []acp.PermissionOption,
	kind acp.PermissionOptionKind,
) (acp.PermissionOption, bool) {
	for _, option := range options {
		if option.Kind == kind {
			return option, true
		}
	}
	return acp.PermissionOption{}, false
}

func selectedPermission(
	option acp.PermissionOption,
) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId),
	}
}
