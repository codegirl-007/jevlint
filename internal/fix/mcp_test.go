package fix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMCPServerUsesHiddenCommand(t *testing.T) {
	t.Parallel()

	server, err := checkMCPServer("/tmp/workspace", "/tmp/workspace/jevlint.json")
	if err != nil {
		t.Fatalf("checkMCPServer() error = %v", err)
	}
	if server.Stdio == nil ||
		server.Stdio.Name != "jevlint" ||
		len(server.Stdio.Args) != 1 ||
		server.Stdio.Args[0] != "mcp-check" {
		t.Fatalf("checkMCPServer() = %#v", server)
	}
	var root, configPath string
	for _, env := range server.Stdio.Env {
		switch env.Name {
		case "JEVLINT_MCP_ROOT":
			root = env.Value
		case "JEVLINT_MCP_CONFIG":
			configPath = env.Value
		}
	}
	if root != "/tmp/workspace" || configPath != "/tmp/workspace/jevlint.json" {
		t.Fatalf("checkMCPServer() env = %#v", server.Stdio.Env)
	}
}

func TestServeCheckListsAndRunsTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "jevlint.json"),
		[]byte(`{
			"languages": {"go": {}},
			"rules": [{
				"id": "database-joins",
				"description": "Join related records in the database.",
				"severity": "error",
				"include": ["**/*.go"]
			}]
		}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "sample.go"),
		[]byte("package sample\n\nfunc JoinInCode() { println(\"join\") }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{
				"model": "jev-test",
				"answers": {
					"database-joins": {
						"type": "choice",
						"choice": "fail",
						"confidence": 1
					}
				}
			}`)
		},
	))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "sk-test")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-test")
	t.Setenv("JEVLINT_MCP_ROOT", root)
	t.Setenv("JEVLINT_MCP_CONFIG", filepath.Join(root, "jevlint.json"))

	var input bytes.Buffer
	writeMCPMessage(&input, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	writeMCPMessage(&input, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	writeMCPMessage(&input, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	writeMCPMessage(&input, []byte(
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"jevlint_check"}}`,
	))

	var output bytes.Buffer
	if err := ServeCheck(context.Background(), &input, &output); err != nil {
		t.Fatalf("ServeCheck() error = %v", err)
	}
	messages := readAllMCPMessages(t, output.Bytes())
	if len(messages) != 3 {
		t.Fatalf("ServeCheck() messages = %d, want 3: %s", len(messages), output.String())
	}
	if !strings.Contains(string(messages[1]), checkToolName) {
		t.Fatalf("tools/list = %s", messages[1])
	}
	if !strings.Contains(string(messages[2]), "database-joins") {
		t.Fatalf("tools/call missing rule: %s", messages[2])
	}
	if !strings.Contains(string(messages[2]), `\"ok\":false`) {
		t.Fatalf("tools/call missing failure flag: %s", messages[2])
	}
}

func readAllMCPMessages(t *testing.T, payload []byte) [][]byte {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(payload))
	messages := make([][]byte, 0)
	for {
		message, err := readMCPMessage(reader)
		if err != nil {
			break
		}
		var decoded map[string]any
		if err := json.Unmarshal(message, &decoded); err != nil {
			t.Fatalf("decode MCP message: %v", err)
		}
		messages = append(messages, message)
	}
	return messages
}
