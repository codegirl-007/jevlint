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
	if len(report.Findings[0].Locations) != 1 ||
		report.Findings[0].Locations[0].Kind != "expression_statement" {
		t.Fatalf("finding locations = %#v", report.Findings[0].Locations)
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
			Locations: []runner.Location{{
				Kind:        "expression_statement",
				Source:      `println("join")`,
				StartLine:   4,
				EndLine:     4,
				StartColumn: 1,
				EndColumn:   16,
			}},
		}},
	}
	var output bytes.Buffer
	writeText(&output, report)

	for _, expected := range []string{
		"✗ ERROR  database-joins",
		"store.go:3-5",
		"Join records in the database.",
		"  3 │ func JoinInCode() {",
		"    │     ^^^^^^^^^^^^^^^",
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

func TestWriteCodeFrameAdjustsColumnsForMidLineSnippet(t *testing.T) {
	t.Parallel()

	finding := runner.Finding{
		Severity:    config.SeverityError,
		Language:    "go",
		StartLine:   3,
		EndLine:     3,
		StartColumn: 4,
		Snippet:     "Ready bool",
		Locations: []runner.Location{{
			StartLine:   3,
			EndLine:     3,
			StartColumn: 4,
			EndColumn:   9,
		}},
	}
	var output bytes.Buffer
	writeCodeFrame(&output, outputStyle{}, finding)

	if !strings.Contains(output.String(), "    │ ^^^^^") {
		t.Fatalf("output = %q, want pointer at start of focused snippet", output.String())
	}
}

func TestWriteSummaryAndTotals(t *testing.T) {
	t.Parallel()

	findings := []runner.Finding{
		{Severity: config.SeverityError},
		{Severity: config.SeverityWarning},
		{Severity: config.SeverityInfo},
	}
	tests := []struct {
		name  string
		color bool
		want  string
	}{
		{
			name: "plain",
			want: "Summary\n  3 findings  1 error  1 warning  1 info\n" +
				"  2 files · 4 code units · 6 evaluations\n",
		},
		{
			name:  "colored",
			color: true,
			want: "\x1b[1mSummary\x1b[0m\n" +
				"  3 findings  \x1b[31m1 error\x1b[0m" +
				"  \x1b[33m1 warning\x1b[0m  \x1b[36m1 info\x1b[0m\n" +
				"  2 files · 4 code units · 6 evaluations\n",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			writeSummary(&output, outputStyle{color: test.color}, findings)
			writeReportTotals(&output, runner.Report{
				ScannedFiles: 2,
				CodeUnits:    4,
				Evaluations:  6,
			})
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestWriteSummaryNoFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		color bool
		want  string
	}{
		{name: "plain", want: "✓ No findings\n"},
		{
			name:  "colored",
			color: true,
			want:  "\x1b[1;32m✓ No findings\x1b[0m\n",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			writeSummary(&output, outputStyle{color: test.color}, nil)
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestWriteReportTotalsIncludesCacheStats(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writeReportTotals(&output, runner.Report{
		ScannedFiles: 2,
		CodeUnits:    4,
		Evaluations:  6,
		Cache: &evaluation.CacheStats{
			Hits:   3,
			Misses: 2,
			Writes: 1,
		},
	})
	want := "  2 files · 4 code units · 6 evaluations\n" +
		"  cache · 3 hits · 2 misses · 1 writes\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestHighlightedLinesEmitsSyntaxColors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"c":      "int main(void) { return 0; }",
		"cpp":    "class Example {};",
		"csharp": "class Example {}",
		"go":     "func main() {}",
		"java":   "class Example {}",
		"kotlin": "class Example",
		"php":    "<?php function main() {}",
		"ruby":   "def main; end",
	}
	for language, source := range tests {
		language, source := language, source
		t.Run(language, func(t *testing.T) {
			t.Parallel()

			lines := highlightedLines(source, language, true)
			if !strings.Contains(strings.Join(lines, "\n"), "\x1b[") {
				t.Fatalf("highlighted lines contain no ANSI colors: %#v", lines)
			}
		})
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

func TestRunRejectsInvalidLanguageConfigurationBeforeAPIKey(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name: "missing languages",
			config: `{
				"rules": [{
					"id": "one",
					"description": "A rule.",
					"severity": "info"
				}]
			}`,
			want: "config must enable at least one language",
		},
		{
			name: "query missing name capture",
			config: `{
				"languages": {
					"go": {
						"functionQueries": ["(function_declaration) @function"]
					}
				},
				"rules": [{
					"id": "one",
					"description": "A rule.",
					"severity": "info"
				}]
			}`,
			want: "go function query must capture @name",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "jevlint.json")
			if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("TYPESAFE_API_KEY", "")

			var stdout, stderr bytes.Buffer
			exitCode := Run(
				context.Background(),
				[]string{"check", "--config", configPath, "."},
				&stdout,
				&stderr,
			)
			if exitCode != 2 {
				t.Fatalf("Run() exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			if strings.Contains(stderr.String(), "TYPESAFE_API_KEY") {
				t.Fatalf("stderr reached API validation: %q", stderr.String())
			}
		})
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

func TestRunRejectsConflictingCacheFlags(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{"check", "--no-cache", "--refresh-cache"},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(
		stderr.String(),
		"--no-cache and --refresh-cache cannot be combined",
	) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunClearsProjectCacheBeforeAPIValidation(t *testing.T) {
	root := writeProject(t, "package sample\n")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "")

	cache, err := evaluation.NewFileCache(root)
	if err != nil {
		t.Fatalf("NewFileCache() error = %v", err)
	}
	if !cache.Put("entry", map[string]evaluation.Result{
		"rule": {Status: evaluation.StatusPass, Confidence: 1},
	}) {
		t.Fatal("Put() = false")
	}

	var stdout, stderr bytes.Buffer
	exitCode := Run(
		context.Background(),
		[]string{
			"check",
			"--config", filepath.Join(root, "jevlint.json"),
			"--clear-cache",
			"--no-cache",
			".",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if _, ok := cache.Get("entry"); ok {
		t.Fatal("cache entry remains after --clear-cache")
	}
	if !strings.Contains(stderr.String(), "TYPESAFE_API_KEY") {
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
		"languages": {"go": {}},
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
