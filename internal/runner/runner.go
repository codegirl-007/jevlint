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
	StartColumn uint              `json:"startColumn"`
	EndColumn   uint              `json:"endColumn"`
	Snippet     string            `json:"snippet"`
	Locations   []Location        `json:"locations,omitempty"`
}

type Location struct {
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	StartLine   uint   `json:"startLine"`
	EndLine     uint   `json:"endLine"`
	StartColumn uint   `json:"startColumn"`
	EndColumn   uint   `json:"endColumn"`
}

type evaluationJob struct {
	rules []config.Rule
	unit  parsing.CodeUnit
}

type evaluationOutcome struct {
	evaluations int
	findings    []pendingFinding
}

type pendingFinding struct {
	finding Finding
	rule    config.Rule
	unit    parsing.CodeUnit
}

type localizationJob struct {
	findingIndex int
	rule         config.Rule
	parent       parsing.CodeUnit
	region       parsing.Region
}

type localizationOutcome struct {
	findingIndex int
	location     *Location
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
			unitRules := make([]config.Rule, 0, len(applicable))
			for _, rule := range applicable {
				if appliesToKind(rule, unit.Kind) {
					unitRules = append(unitRules, rule)
				}
			}
			if len(unitRules) == 0 {
				continue
			}
			jobs = append(jobs, evaluationJob{
				rules: unitRules,
				unit:  unit,
			})
		}
	}

	outcomes, err := evaluateJobs(ctx, runner.Evaluator, jobs, concurrency)
	if err != nil {
		return Report{}, err
	}
	pending := make([]pendingFinding, 0)
	for _, outcome := range outcomes {
		report.Evaluations += outcome.evaluations
		pending = append(pending, outcome.findings...)
	}

	localizationEvaluations, err := localizeFindings(
		ctx,
		runner.Evaluator,
		pending,
		concurrency,
	)
	if err != nil {
		return Report{}, err
	}
	report.Evaluations += localizationEvaluations
	for _, item := range pending {
		report.Findings = append(report.Findings, item.finding)
	}
	return report, nil
}

func appliesToKind(rule config.Rule, kind parsing.CodeKind) bool {
	if len(rule.Kinds) == 0 {
		return true
	}
	for _, allowed := range rule.Kinds {
		if allowed == string(kind) {
			return true
		}
	}
	return false
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
		findings: make([]pendingFinding, 0),
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
		outcome.findings = append(outcome.findings, pendingFinding{
			finding: Finding{
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
				StartColumn: job.unit.StartColumn,
				EndColumn:   job.unit.EndColumn,
				Snippet:     job.unit.Source,
			},
			rule: rule,
			unit: job.unit,
		})
	}
	return outcome, nil
}

func localizeFindings(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	findings []pendingFinding,
	concurrency int,
) (int, error) {
	jobs := make([]localizationJob, 0)
	for findingIndex, item := range findings {
		for _, region := range item.unit.Regions {
			if !localizesTo(item.rule, region.Category) {
				continue
			}
			jobs = append(jobs, localizationJob{
				findingIndex: findingIndex,
				rule:         item.rule,
				parent:       item.unit,
				region:       region,
			})
		}
	}
	if len(jobs) == 0 {
		return 0, nil
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}

	localizationContext, cancel := context.WithCancel(ctx)
	defer cancel()

	indices := make(chan int, len(jobs))
	for index := range jobs {
		indices <- index
	}
	close(indices)

	outcomes := make([]localizationOutcome, len(jobs))
	var workers sync.WaitGroup
	var errorOnce sync.Once
	var firstError error

	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for index := range indices {
				if localizationContext.Err() != nil {
					return
				}
				outcome, err := evaluateLocalizationJob(
					localizationContext,
					evaluator,
					jobs[index],
				)
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
		return 0, firstError
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for _, outcome := range outcomes {
		if outcome.location != nil {
			item := &findings[outcome.findingIndex]
			item.finding.Locations = append(item.finding.Locations, *outcome.location)
		}
	}
	return len(jobs), nil
}

func evaluateLocalizationJob(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	job localizationJob,
) (localizationOutcome, error) {
	candidate := parsing.CodeUnit{
		Kind:         parsing.CodeKindRegion,
		Name:         job.parent.Name + ":" + job.region.Kind,
		Language:     job.parent.Language,
		Path:         job.parent.Path,
		Source:       job.region.Source,
		ParentSource: job.parent.Source,
		StartLine:    job.region.StartLine,
		EndLine:      job.region.EndLine,
		StartColumn:  job.region.StartColumn,
		EndColumn:    job.region.EndColumn,
		StartByte:    job.region.StartByte,
		EndByte:      job.region.EndByte,
		RelatedTypes: job.parent.RelatedTypes,
	}
	results, err := evaluator.Evaluate(ctx, evaluation.Batch{
		Rules:    []config.Rule{job.rule},
		CodeUnit: candidate,
	})
	if err != nil {
		return localizationOutcome{}, fmt.Errorf(
			"localize rule %q at %s:%d: %w",
			job.rule.ID,
			job.parent.Path,
			job.region.StartLine,
			err,
		)
	}
	result, ok := results[job.rule.ID]
	if !ok {
		return localizationOutcome{}, fmt.Errorf(
			"invalid localization result for rule %q at %s:%d: result is missing",
			job.rule.ID,
			job.parent.Path,
			job.region.StartLine,
		)
	}
	if err := result.Validate(); err != nil {
		return localizationOutcome{}, fmt.Errorf(
			"invalid localization result for rule %q at %s:%d: %w",
			job.rule.ID,
			job.parent.Path,
			job.region.StartLine,
			err,
		)
	}

	outcome := localizationOutcome{findingIndex: job.findingIndex}
	if result.Status == evaluation.StatusFail {
		outcome.location = &Location{
			Category:    job.region.Category,
			Kind:        job.region.Kind,
			Source:      job.region.Source,
			StartLine:   job.region.StartLine,
			EndLine:     job.region.EndLine,
			StartColumn: job.region.StartColumn,
			EndColumn:   job.region.EndColumn,
		}
	}
	return outcome, nil
}

func localizesTo(rule config.Rule, category string) bool {
	if rule.Localize == nil {
		return true
	}
	for _, allowed := range rule.Localize {
		if allowed == category {
			return true
		}
	}
	return false
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
