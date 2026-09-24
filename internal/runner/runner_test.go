package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
)

type recordingEvaluator struct {
	calls   int
	batches []evaluation.Batch
}

type barrierEvaluator struct {
	started chan struct{}
	release chan struct{}
}

func (evaluator *barrierEvaluator) Evaluate(
	ctx context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	select {
	case evaluator.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-evaluator.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	results := make(map[string]evaluation.Result, len(batch.Rules))
	for _, rule := range batch.Rules {
		results[rule.ID] = evaluation.Result{
			Status:     evaluation.StatusPass,
			Confidence: 1,
		}
	}
	return results, nil
}

func (evaluator *recordingEvaluator) Evaluate(
	_ context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	evaluator.calls++
	evaluator.batches = append(evaluator.batches, batch)
	databaseStatus := evaluation.StatusPass
	if batch.CodeUnit.Kind == parsing.CodeKindFunction {
		databaseStatus = evaluation.StatusFail
	}
	return map[string]evaluation.Result{
		"database-joins": {
			Status:     databaseStatus,
			Confidence: 0.91,
		},
		"semicolons": {
			Status:     evaluation.StatusPass,
			Confidence: 0.99,
		},
	}, nil
}

func TestCheckBatchesRulesPerFunctionAndRetainsSnippet(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := "package sample\n\ntype User struct{}\n\n" +
		"func JoinUsers(user User) {\n\tprintln(\"join\")\n}\n"
	if err := os.WriteFile(filepath.Join(root, "store.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := config.Config{Rules: []config.Rule{
		{
			ID:          "database-joins",
			Description: "Join records in the database.",
			Severity:    config.SeverityError,
		},
		{
			ID:          "semicolons",
			Description: "Every line ends with a semicolon.",
			Severity:    config.SeverityWarning,
		},
	}}
	evaluator := &recordingEvaluator{}
	report, err := (Runner{
		Extractor: parsing.NewExtractor(),
		Evaluator: evaluator,
	}).Check(context.Background(), cfg, Options{Root: root, Concurrency: 1})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	if evaluator.calls != 2 {
		t.Fatalf("evaluator calls = %d, want 2", evaluator.calls)
	}
	if len(evaluator.batches) != 2 ||
		len(evaluator.batches[0].Rules) != 2 ||
		len(evaluator.batches[1].Rules) != 2 {
		t.Fatalf("batches = %#v", evaluator.batches)
	}
	if evaluator.batches[0].CodeUnit.Kind != parsing.CodeKindType ||
		evaluator.batches[1].CodeUnit.Kind != parsing.CodeKindFunction {
		t.Fatalf(
			"batch kinds = %s, %s",
			evaluator.batches[0].CodeUnit.Kind,
			evaluator.batches[1].CodeUnit.Kind,
		)
	}
	if report.Evaluations != 4 {
		t.Fatalf("evaluations = %d, want 4", report.Evaluations)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	finding := report.Findings[0]
	if finding.Description != "Join records in the database." {
		t.Fatalf("description = %q", finding.Description)
	}
	if finding.Kind != parsing.CodeKindFunction || finding.Name != "JoinUsers" {
		t.Fatalf("finding target = %s %q", finding.Kind, finding.Name)
	}
	if !strings.Contains(finding.Snippet, `func JoinUsers(user User)`) {
		t.Fatalf("snippet = %q", finding.Snippet)
	}
}

func TestCheckEvaluatesFunctionsConcurrently(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n\nfunc First() {}\n\nfunc Second() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := config.Config{Rules: []config.Rule{{
		ID:          "rule",
		Description: "A test rule.",
		Severity:    config.SeverityError,
	}}}
	evaluator := &barrierEvaluator{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}

	type checkResult struct {
		report Report
		err    error
	}
	done := make(chan checkResult, 1)
	go func() {
		report, err := (Runner{
			Extractor: parsing.NewExtractor(),
			Evaluator: evaluator,
		}).Check(context.Background(), cfg, Options{
			Root:        root,
			Concurrency: 2,
		})
		done <- checkResult{report: report, err: err}
	}()

	for range 2 {
		select {
		case <-evaluator.started:
		case <-time.After(time.Second):
			t.Fatal("two evaluations did not run concurrently")
		}
	}
	close(evaluator.release)

	result := <-done
	if result.err != nil {
		t.Fatalf("Check() error = %v", result.err)
	}
	if result.report.Evaluations != 2 {
		t.Fatalf("evaluations = %d, want 2", result.report.Evaluations)
	}
}
