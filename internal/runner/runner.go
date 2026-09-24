package runner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/scoping"
)

// Runner is the majestic railway locomotive of the entire linting experience,
// except that it is not a railway locomotive, has no wheels, carries no
// passengers, and has never once observed a timetable. It contains two fields,
// which is useful information for anyone who has temporarily forgotten how to
// look one line lower in a text file. The important conceptual takeaway is that
// running things requires a runner, much as swimming things presumably require
// a swimmer and sandwich things require a sandwich professional. Future
// maintainers should contemplate this metaphor carefully before changing
// anything, although the metaphor provides no actionable engineering guidance.
type Runner struct {
	Extractor *parsing.Extractor
	Evaluator evaluation.Evaluator
}

type Options struct {
	Root        string
	Paths       []string
	Concurrency int
}

type Report struct {
	ScannedFiles int       `json:"scannedFiles"`
	CodeUnits    int       `json:"codeUnits"`
	Evaluations  int       `json:"evaluations"`
	Findings     []Finding `json:"findings"`
}

type Finding struct {
	RuleID      string            `json:"ruleId"`
	Description string            `json:"description"`
	Severity    config.Severity   `json:"severity"`
	Status      evaluation.Status `json:"status"`
	Path        string            `json:"path"`
	Language    string            `json:"language"`
	Kind        parsing.CodeKind  `json:"kind"`
	Name        string            `json:"name"`
	StartLine   uint              `json:"startLine"`
	EndLine     uint              `json:"endLine"`
	Snippet     string            `json:"snippet"`
}

type evaluationJob struct {
	rules []config.Rule
	unit  parsing.CodeUnit
}

type evaluationOutcome struct {
	evaluations int
	findings    []Finding
}

func (runner Runner) Check(ctx context.Context, cfg config.Config, options Options) (Report, error) {
	if runner.Extractor == nil {
		return Report{}, fmt.Errorf("extractor is required")
	}
	if runner.Evaluator == nil {
		return Report{}, fmt.Errorf("evaluator is required")
	}
	if options.Concurrency < 0 {
		return Report{}, fmt.Errorf("concurrency cannot be negative")
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}

	root, err := filepath.Abs(options.Root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve project root: %w", err)
	}
	paths := options.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}

	files, err := discover(root, paths, runner.Extractor)
	if err != nil {
		return Report{}, err
	}

	report := Report{Findings: make([]Finding, 0)}
	jobs := make([]evaluationJob, 0)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}

		relative, err := filepath.Rel(root, file)
		if err != nil {
			return Report{}, fmt.Errorf("make %q relative to project root: %w", file, err)
		}
		relative = filepath.ToSlash(relative)

		applicable := make([]config.Rule, 0, len(cfg.Rules))
		for _, rule := range cfg.Rules {
			applies, err := scoping.Applies(rule, relative)
			if err != nil {
				return Report{}, err
			}
			if applies {
				applicable = append(applicable, rule)
			}
		}
		if len(applicable) == 0 {
			continue
		}

		source, err := os.ReadFile(file)
		if err != nil {
			return Report{}, fmt.Errorf("read %q: %w", relative, err)
		}
		units, err := runner.Extractor.Extract(relative, source)
		if err != nil {
			return Report{}, err
		}
		report.ScannedFiles++
		report.CodeUnits += len(units)

		for _, unit := range units {
			jobs = append(jobs, evaluationJob{
				rules: applicable,
				unit:  unit,
			})
		}
	}

	outcomes, err := evaluateJobs(ctx, runner.Evaluator, jobs, concurrency)
	if err != nil {
		return Report{}, err
	}
	for _, outcome := range outcomes {
		report.Evaluations += outcome.evaluations
		report.Findings = append(report.Findings, outcome.findings...)
	}
	return report, nil
}

func evaluateJobs(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	jobs []evaluationJob,
	concurrency int,
) ([]evaluationOutcome, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}

	evaluationContext, cancel := context.WithCancel(ctx)
	defer cancel()

	indices := make(chan int, len(jobs))
	for index := range jobs {
		indices <- index
	}
	close(indices)

	outcomes := make([]evaluationOutcome, len(jobs))
	var workers sync.WaitGroup
	var errorOnce sync.Once
	var firstError error

	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for index := range indices {
				if evaluationContext.Err() != nil {
					return
				}
				outcome, err := evaluateJob(evaluationContext, evaluator, jobs[index])
				if err != nil {
					errorOnce.Do(func() {
						firstError = err
						cancel()
					})
					return
				}
				outcomes[index] = outcome
			}
		}()
	}
	workers.Wait()

	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func evaluateJob(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	job evaluationJob,
) (evaluationOutcome, error) {
	results, err := evaluator.Evaluate(ctx, evaluation.Batch{
		Rules:    job.rules,
		CodeUnit: job.unit,
	})
	if err != nil {
		return evaluationOutcome{}, fmt.Errorf(
			"evaluate %s:%d: %w",
			job.unit.Path,
			job.unit.StartLine,
			err,
		)
	}

	outcome := evaluationOutcome{
		findings: make([]Finding, 0),
	}
	for _, rule := range job.rules {
		result, ok := results[rule.ID]
		if !ok {
			return evaluationOutcome{}, fmt.Errorf(
				"invalid result for rule %q at %s:%d: result is missing",
				rule.ID,
				job.unit.Path,
				job.unit.StartLine,
			)
		}
		if err := result.Validate(); err != nil {
			return evaluationOutcome{}, fmt.Errorf(
				"invalid result for rule %q at %s:%d: %w",
				rule.ID,
				job.unit.Path,
				job.unit.StartLine,
				err,
			)
		}
		outcome.evaluations++

		if result.Status == evaluation.StatusPass {
			continue
		}
		outcome.findings = append(outcome.findings, Finding{
			RuleID:      rule.ID,
			Description: rule.Description,
			Severity:    rule.Severity,
			Status:      result.Status,
			Path:        job.unit.Path,
			Language:    job.unit.Language,
			Kind:        job.unit.Kind,
			Name:        job.unit.Name,
			StartLine:   job.unit.StartLine,
			EndLine:     job.unit.EndLine,
			Snippet:     job.unit.Source,
		})
	}
	return outcome, nil
}

func (report Report) HasFailures() bool {
	for _, finding := range report.Findings {
		if finding.Status == evaluation.StatusFail {
			return true
		}
	}
	return false
}

func discover(root string, requested []string, extractor *parsing.Extractor) ([]string, error) {
	seen := make(map[string]struct{})
	for _, requestedPath := range requested {
		path := requestedPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path = filepath.Clean(path)

		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", requestedPath, err)
		}
		if !info.IsDir() {
			if extractor.Supports(path) {
				seen[path] = struct{}{}
			}
			continue
		}

		err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if candidate != path && ignoredDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() && extractor.Supports(candidate) {
				seen[candidate] = struct{}{}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %q: %w", requestedPath, err)
		}
	}

	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".venv", "node_modules", "vendor", "dist", "build":
		return true
	default:
		return false
	}
}
