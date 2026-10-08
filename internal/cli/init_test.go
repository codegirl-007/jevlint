package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesStarterFiles(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, dir, "a.go", "package sample\n\nfunc A() {}\n")
	writeProjectFile(t, dir, "b.ts", "export function b() {}\n")
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit = %d; stderr = %q", code, stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, ".jevlint.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var document struct {
		Languages map[string]json.RawMessage `json:"languages"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode config: %v\n%s", err, data)
	}
	for _, language := range []string{"go", "typescript"} {
		if _, ok := document.Languages[language]; !ok {
			t.Errorf("config languages = %#v, want %q", document.Languages, language)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf("init should not create .env (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("init should not create .gitignore (stat err = %v)", err)
	}
}

func TestInitLeavesGitignoreAlone(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, dir, "a.go", "package sample\n")
	const original = "node_modules\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit = %d; stderr = %q", code, stderr.String())
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != original {
		t.Fatalf(".gitignore = %q, want it unchanged", ignore)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf("init should not create .env (stat err = %v)", err)
	}
}

func TestInitRefusesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, dir, "a.go", "package sample\n")
	if err := os.WriteFile(
		filepath.Join(dir, "jevlint.json"),
		[]byte(`{"languages": {"go": {}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init"}, &stdout, &stderr); code != 2 {
		t.Fatalf("init exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %q, want an already-exists error", stderr.String())
	}
	if strings.Contains(stdout.String(), "defaulting") {
		t.Fatalf("stdout = %q, should not default before the existing-config check", stdout.String())
	}
}

func TestInitJSONOutput(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, dir, "a.go", "package sample\n\nfunc A() {}\n")
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init --json exit = %d; stderr = %q", code, stderr.String())
	}
	var report struct {
		Config    string   `json:"config"`
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode init JSON: %v\n%s", err, stdout.String())
	}
	if !strings.HasSuffix(report.Config, ".jevlint.json") {
		t.Fatalf("report = %#v", report)
	}
	if len(report.Languages) != 1 || report.Languages[0] != "go" {
		t.Fatalf("languages = %#v, want [go]", report.Languages)
	}
	if strings.Contains(stdout.String(), "Next:") {
		t.Fatalf("JSON output should not include human text:\n%s", stdout.String())
	}
}

func TestInitLanguagesFlag(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init", "--languages", "rust"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit = %d; stderr = %q", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".jevlint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"rust"`) {
		t.Fatalf("config = %s, want rust", data)
	}
}

func TestInitHelpListsSupportedLanguages(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init --help exit = %d", code)
	}
	out := stderr.String()
	if !strings.Contains(out, "supported:") {
		t.Fatalf("help missing supported list:\n%s", out)
	}
	for _, language := range []string{"c", "csharp", "go", "javascript", "rust", "typescript"} {
		if !strings.Contains(out, language) {
			t.Errorf("help missing language %q:\n%s", language, out)
		}
	}
}

func TestInitDefaultsLanguageWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit = %d; stderr = %q", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".jevlint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"go"`) {
		t.Fatalf("config = %s, want the go default", data)
	}
	if !strings.Contains(stdout.String(), "defaulting to go") {
		t.Fatalf("stdout = %q, want a default note", stdout.String())
	}
}
