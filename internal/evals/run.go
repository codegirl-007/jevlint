package evals

import (
	"context"
	"errors"
	"fmt"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

var ErrNoApplicableUnits = errors.New("no applicable code units")

type Result struct {
	Rule       string   `json:"rule"`
	Name       string   `json:"name,omitempty"`
	File       string   `json:"file"`
	Expected   Expect   `json:"expected"`
	Actual     Expect   `json:"actual"`
	Matched    bool     `json:"matched"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type Report struct {
	Total      int      `json:"total"`
	Matched    int      `json:"matched"`
	Mismatched int      `json:"mismatched"`
	Cases      []Result `json:"cases"`
}

type Options struct {
	Root        string
	Concurrency int
}

func Run(
	ctx context.Context,
	document Document,
	cfg config.Config,
	extractor *parsing.Extractor,
	evaluator evaluation.Evaluator,
	options Options,
) (Report, error) {
	report := Report{Cases: make([]Result, 0, len(document.Cases))}
	for _, evalCase := range document.Cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		result, err := runCase(ctx, evalCase, cfg, extractor, evaluator, options)
		if err != nil {
			return Report{}, err
		}
		report.Total++
		if result.Matched {
			report.Matched++
		} else {
			report.Mismatched++
		}
		report.Cases = append(report.Cases, result)
	}
	return report, nil
}

func runCase(
	ctx context.Context,
	evalCase Case,
	cfg config.Config,
	extractor *parsing.Extractor,
	evaluator evaluation.Evaluator,
	options Options,
) (Result, error) {
	rule, ok := ruleByID(cfg, evalCase.Rule)
	if !ok {
		return Result{}, fmt.Errorf("unknown eval rule %q", evalCase.Rule)
	}
	recorder := &recordingEvaluator{inner: evaluator}
	check := runner.Runner{
		Extractor: extractor,
		Evaluator: recorder,
	}
	report, err := check.Evaluate(ctx, evalConfig(cfg, rule), runner.Options{
		Root:        options.Root,
		Paths:       []string{evalCase.AbsolutePath()},
		Concurrency: options.Concurrency,
	})
	if err != nil {
		return Result{}, fmt.Errorf("evaluate %s: %w", caseLabel(evalCase), err)
	}
	if report.Evaluations == 0 {
		return Result{}, fmt.Errorf("%s: %w", caseLabel(evalCase), ErrNoApplicableUnits)
	}
	actual := ExpectPass
	if len(report.Findings) > 0 {
		actual = ExpectFail
	}
	result := Result{
		Rule:       evalCase.Rule,
		Name:       evalCase.Name,
		File:       evalCase.File,
		Expected:   evalCase.Expect,
		Actual:     actual,
		Matched:    evalCase.Expect == actual,
		Confidence: recorder.reportableConfidence(evalCase.Rule, cfg.MinimumConfidence()),
	}
	return result, nil
}

func evalConfig(cfg config.Config, rule config.Rule) config.Config {
	rule.Include = nil
	rule.Exclude = nil
	return config.Config{
		Languages:     cfg.Languages,
		MinConfidence: cfg.MinConfidence,
		Rules:         []config.Rule{rule},
	}
}

type recordingEvaluator struct {
	inner    evaluation.Evaluator
	bestFail map[string]float64
}

func (recorder *recordingEvaluator) Evaluate(
	ctx context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	results, err := recorder.inner.Evaluate(ctx, batch)
	if err != nil {
		return nil, err
	}
	if recorder.bestFail == nil {
		recorder.bestFail = make(map[string]float64)
	}
	for id, result := range results {
		if result.Status != evaluation.StatusFail {
			continue
		}
		current, exists := recorder.bestFail[id]
		if !exists || result.Confidence > current {
			recorder.bestFail[id] = result.Confidence
		}
	}
	return results, nil
}

func (recorder *recordingEvaluator) CacheStats() evaluation.CacheStats {
	provider, ok := recorder.inner.(evaluation.CacheStatsProvider)
	if !ok {
		return evaluation.CacheStats{}
	}
	return provider.CacheStats()
}

func (recorder *recordingEvaluator) reportableConfidence(
	ruleID string,
	floor float64,
) *float64 {
	confidence, ok := recorder.bestFail[ruleID]
	if !ok || confidence < floor {
		return nil
	}
	value := confidence
	return &value
}
