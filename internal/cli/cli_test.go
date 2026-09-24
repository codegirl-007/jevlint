package cli

import (
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

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

func TestRunReportsJevFailureAsJSON(t *testing.T) {
	root := writeProject(t, `
package sample

func JoinInCode() {
	println("join")
}
`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"model": "jev-test",
			"answers": {
				"database-joins": {
					"type": "choice",
					"choice": "fail",
					"confidence": 0.92
				}
			}
		}`)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "sk-test")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-test")

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{"check", "--config", filepath.Join(root, "jevlint.json"), "--format", "json", "."},
		&stdout,
		&stderr,
	)
	if exitCode != 1 {
		t.Fatalf("Run() exit code = %d, want 1; stderr = %q", exitCode, stderr.String())
	}

	var report runner.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode output: %v\noutput: %s", err, stdout.String())
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %#v, want one", report.Findings)
	}
	if report.Findings[0].RuleID != "database-joins" {
		t.Fatalf("rule id = %q", report.Findings[0].RuleID)
	}
	if !strings.Contains(report.Findings[0].Snippet, "func JoinInCode()") {
		t.Fatalf("snippet = %q", report.Findings[0].Snippet)
	}
	if report.Findings[0].Kind != parsing.CodeKindFunction ||
		report.Findings[0].Name != "JoinInCode" {
		t.Fatalf("finding target = %s %q", report.Findings[0].Kind, report.Findings[0].Name)
	}
	if bytes.Contains(stdout.Bytes(), []byte(`"confidence"`)) {
		t.Fatalf("JSON output exposes confidence: %s", stdout.String())
	}
}

func TestWriteTextHighlightsRuleAndSnippet(t *testing.T) {
	t.Parallel()

	report := runner.Report{
		ScannedFiles: 1,
		CodeUnits:    1,
		Evaluations:  1,
		Findings: []runner.Finding{{
			RuleID:      "database-joins",
			Description: "Join records in the database.",
			Severity:    config.SeverityError,
			Status:      evaluation.StatusFail,
			Path:        "store.go",
			Kind:        parsing.CodeKindFunction,
			Name:        "JoinInCode",
			StartLine:   3,
			EndLine:     5,
			Snippet:     "func JoinInCode() {\n\tprintln(\"join\")\n}",
		}},
	}
	var output bytes.Buffer
	writeText(&output, report)

	for _, expected := range []string{
		"✗ ERROR  database-joins",
		"store.go:3-5",
		"Join records in the database.",
		"  │ func JoinInCode() {",
		"Summary",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output = %q, want %q", output.String(), expected)
		}
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("plain output contains ANSI escapes: %q", output.String())
	}
	if strings.Contains(output.String(), "confidence") {
		t.Fatalf("plain output exposes confidence: %q", output.String())
	}

	var colored bytes.Buffer
	writeTextStyled(&colored, report, true)
	if !strings.Contains(colored.String(), "\x1b[1;31m") {
		t.Fatalf("colored output lacks error color: %q", colored.String())
	}
	if !strings.Contains(
		colored.String(),
		"\x1b[1;31mJoin records in the database.\x1b[0m",
	) {
		t.Fatalf("colored output does not highlight description: %q", colored.String())
	}
}

func TestRunRequiresAPIKey(t *testing.T) {
	root := writeProject(t, "package sample\n")
	t.Setenv("TYPESAFE_API_KEY", "")

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{"check", "--config", filepath.Join(root, "jevlint.json"), "."},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "TYPESAFE_API_KEY") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsInvalidConcurrency(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{"check", "--concurrency", "0"},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "--concurrency must be at least 1") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsInvalidColorMode(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{"check", "--color", "sparkles"},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "--color must be auto, always, or never") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func writeProject(t *testing.T, source string) string {
	t.Helper()

	root := t.TempDir()
	config := `{
		"rules": [{
			"id": "database-joins",
			"description": "Join related records in the database.",
			"severity": "error",
			"include": ["**/*.go"]
		}]
	}`
	if err := os.WriteFile(filepath.Join(root, "jevlint.json"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return root
}
