package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
)

func TestLoadRejectsUnknownRule(t *testing.T) {
	t.Parallel()

	root := writeEvalDir(t, `{
		"version": 1,
		"cases": [{"rule": "missing-rule", "file": "sample.go", "expect": "fail"}]
	}`, "package sample\n\nfunc Ready() {}\n")
	_, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err == nil || !strings.Contains(err.Error(), "unknown eval rule") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DefaultFile), []byte(validEvalsJSON("missing.go")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err == nil || !strings.Contains(err.Error(), "eval fixture") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsUnsupportedLanguage(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DefaultFile), []byte(validEvalsJSON("notes.txt")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err == nil || !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInvalidExpect(t *testing.T) {
	t.Parallel()

	root := writeEvalDir(t, `{
		"version": 1,
		"cases": [{
			"rule": "database-joins",
			"file": "sample.go",
			"expect": "maybe"
		}]
	}`, "package sample\n\nfunc Ready() {}\n")
	_, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err == nil || !strings.Contains(err.Error(), "expect must be") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsDuplicateRuleAndFile(t *testing.T) {
	t.Parallel()

	root := writeEvalDir(t, `{
		"version": 1,
		"cases": [
			{"name": "first", "rule": "database-joins", "file": "sample.go", "expect": "pass"},
			{"name": "second", "rule": "database-joins", "file": "./sample.go", "expect": "fail"}
		]
	}`, "package sample\n\nfunc Ready() {}\n")
	_, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err == nil || !strings.Contains(err.Error(), "duplicate eval case") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadResolvesFixturesRelativeToEvalFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	casesDir := filepath.Join(root, "cases")
	if err := os.Mkdir(casesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(casesDir, DefaultFile), []byte(validEvalsJSON("join.go")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(casesDir, "join.go"), []byte("package sample\n\nfunc Ready() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := Load(filepath.Join(casesDir, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if document.Cases[0].AbsolutePath() != filepath.Join(casesDir, "join.go") {
		t.Fatalf("abs = %q", document.Cases[0].AbsolutePath())
	}
}

func TestRunPassAndFailCases(t *testing.T) {
	t.Parallel()

	root := writeEvalDir(t, `{
		"version": 1,
		"cases": [
			{"name": "should-pass", "rule": "database-joins", "file": "sample.go", "expect": "pass"},
			{"name": "should-fail", "rule": "database-joins", "file": "other.go", "expect": "fail"}
		]
	}`, "package sample\n\nfunc Ready() {}\n")
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte("package sample\n\nfunc Other() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	passReport, err := Run(
		context.Background(),
		document,
		sampleConfig(nil),
		testExtractor(t),
		fixedEvaluator{status: evaluation.StatusPass, confidence: 0.9},
		Options{Root: root, Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("Run() pass error = %v", err)
	}
	if passReport.Matched != 1 || passReport.Mismatched != 1 {
		t.Fatalf("pass report = %#v", passReport)
	}
	if passReport.Cases[0].Confidence != nil {
		t.Fatalf("pass confidence = %v", passReport.Cases[0].Confidence)
	}

	failReport, err := Run(
		context.Background(),
		document,
		sampleConfig(nil),
		testExtractor(t),
		fixedEvaluator{status: evaluation.StatusFail, confidence: 0.91},
		Options{Root: root, Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("Run() fail error = %v", err)
	}
	if failReport.Matched != 1 || failReport.Mismatched != 1 {
		t.Fatalf("fail report = %#v", failReport)
	}
	if failReport.Cases[1].Confidence == nil || *failReport.Cases[1].Confidence != 0.91 {
		t.Fatalf("fail confidence = %v", failReport.Cases[1].Confidence)
	}
}

func TestRunEvaluatesExcludedFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixtures := filepath.Join(root, "fixtures")
	if err := os.Mkdir(fixtures, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DefaultFile), []byte(validEvalsJSON("fixtures/sample.go")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtures, "sample.go"), []byte("package sample\n\nfunc Ready() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := sampleConfig([]string{"fixtures/**"})
	document, err := Load(filepath.Join(root, DefaultFile), cfg, testExtractor(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	report, err := Run(
		context.Background(),
		document,
		cfg,
		testExtractor(t),
		fixedEvaluator{status: evaluation.StatusFail, confidence: 1},
		Options{Root: root, Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !report.Cases[0].Matched || report.Cases[0].Actual != ExpectFail {
		t.Fatalf("report = %#v", report)
	}
}

func TestRunRequiresApplicableUnits(t *testing.T) {
	t.Parallel()

	root := writeEvalDir(t, validEvalsJSON("sample.go"), "package sample\n")
	document, err := Load(filepath.Join(root, DefaultFile), sampleConfig(nil), testExtractor(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = Run(
		context.Background(),
		document,
		sampleConfig(nil),
		testExtractor(t),
		fixedEvaluator{status: evaluation.StatusPass, confidence: 1},
		Options{Root: root, Concurrency: 1},
	)
	if !errors.Is(err, ErrNoApplicableUnits) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestResultJSONIncludesConfidence(t *testing.T) {
	t.Parallel()

	confidence := 0.88
	data, err := json.Marshal(Result{
		Rule:       "database-joins",
		File:       "sample.go",
		Expected:   ExpectFail,
		Actual:     ExpectFail,
		Matched:    true,
		Confidence: &confidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"confidence":0.88`) {
		t.Fatalf("json = %s", data)
	}
}

type fixedEvaluator struct {
	status     evaluation.Status
	confidence float64
}

func (evaluator fixedEvaluator) Evaluate(
	_ context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	results := make(map[string]evaluation.Result, len(batch.Rules))
	for _, rule := range batch.Rules {
		results[rule.ID] = evaluation.Result{
			Status:     evaluator.status,
			Confidence: evaluator.confidence,
		}
	}
	return results, nil
}

func sampleConfig(exclude []string) config.Config {
	return config.Config{
		Languages: map[string]config.Language{"go": {}},
		Rules: []config.Rule{{
			ID:          "database-joins",
			Description: "Join related records in the database.",
			Severity:    config.SeverityError,
			Include:     []string{"src/**/*.go"},
			Exclude:     exclude,
		}},
	}
}

func testExtractor(t *testing.T) *parsing.Extractor {
	t.Helper()
	extractor, err := parsing.NewExtractor(map[string]config.Language{"go": {}})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}
	return extractor
}

func writeEvalDir(t *testing.T, evalsJSON string, source string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DefaultFile), []byte(evalsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func validEvalsJSON(file string) string {
	return `{
		"version": 1,
		"cases": [{
			"rule": "database-joins",
			"file": "` + file + `",
			"expect": "fail"
		}]
	}`
}
