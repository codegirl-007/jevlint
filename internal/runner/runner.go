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

type checkSetup struct {
	root        string
	paths       []string
	concurrency int
}

func (runner Runner) Check(ctx context.Context, cfg config.Config, options Options) (Report, error) {
	setup, err := runner.prepareCheck(options)
	if err != nil {
		return Report{}, err
	}
	files, err := discover(setup.root, setup.paths, runner.Extractor)
	if err != nil {
		return Report{}, err
	}
	report, jobs, err := runner.planEvaluations(ctx, cfg, setup.root, files)
	if err != nil {
		return Report{}, err
	}
	outcomes, err := evaluateJobs(ctx, runner.Evaluator, jobs, setup.concurrency)
	if err != nil {
		return Report{}, err
	}
	pending := collectOutcomes(&report, outcomes)
	localizationEvaluations, err := localizeFindings(
		ctx,
		runner.Evaluator,
		pending,
		setup.concurrency,
	)
	if err != nil {
		return Report{}, err
	}
	report.Evaluations += localizationEvaluations
	appendFindings(&report, pending)
	return report, nil
}

func (runner Runner) prepareCheck(options Options) (checkSetup, error) {
	if runner.Extractor == nil {
		return checkSetup{}, fmt.Errorf("extractor is required")
	}
	if runner.Evaluator == nil {
		return checkSetup{}, fmt.Errorf("evaluator is required")
	}
	if options.Concurrency < 0 {
		return checkSetup{}, fmt.Errorf("concurrency cannot be negative")
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}

	root, err := filepath.Abs(options.Root)
	if err != nil {
		return checkSetup{}, fmt.Errorf("resolve project root: %w", err)
	}
	paths := options.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return checkSetup{root: root, paths: paths, concurrency: concurrency}, nil
}

func (runner Runner) planEvaluations(
	ctx context.Context,
	cfg config.Config,
	root string,
	files []string,
) (Report, []evaluationJob, error) {
	report := Report{Findings: make([]Finding, 0)}
	jobs := make([]evaluationJob, 0)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Report{}, nil, err
		}
		fileJobs, codeUnits, scanned, err := runner.planFile(cfg, root, file)
		if err != nil {
			return Report{}, nil, err
		}
		if scanned {
			report.ScannedFiles++
		}
		report.CodeUnits += codeUnits
		jobs = append(jobs, fileJobs...)
	}
	return report, jobs, nil
}

func (runner Runner) planFile(
	cfg config.Config,
	root string,
	file string,
) ([]evaluationJob, int, bool, error) {
	relative, err := filepath.Rel(root, file)
	if err != nil {
		return nil, 0, false, fmt.Errorf(
			"make %q relative to project root: %w",
			file,
			err,
		)
	}
	relative = filepath.ToSlash(relative)
	applicable, err := applicableRules(cfg.Rules, relative)
	if err != nil || len(applicable) == 0 {
		return nil, 0, false, err
	}
	source, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read %q: %w", relative, err)
	}
	units, err := runner.Extractor.Extract(relative, source)
	if err != nil {
		return nil, 0, false, err
	}
	return jobsForUnits(units, applicable), len(units), true, nil
}

func applicableRules(rules []config.Rule, path string) ([]config.Rule, error) {
	applicable := make([]config.Rule, 0, len(rules))
	for _, rule := range rules {
		applies, err := scoping.Applies(rule, path)
		if err != nil {
			return nil, err
		}
		if applies {
			applicable = append(applicable, rule)
		}
	}
	return applicable, nil
}

func jobsForUnits(units []parsing.CodeUnit, rules []config.Rule) []evaluationJob {
	jobs := make([]evaluationJob, 0, len(units))
	for _, unit := range units {
		unitRules := make([]config.Rule, 0, len(rules))
		for _, rule := range rules {
			if appliesToKind(rule, unit.Kind) {
				unitRules = append(unitRules, rule)
			}
		}
		if len(unitRules) > 0 {
			jobs = append(jobs, evaluationJob{rules: unitRules, unit: unit})
		}
	}
	return jobs
}

func collectOutcomes(
	report *Report,
	outcomes []evaluationOutcome,
) []pendingFinding {
	pending := make([]pendingFinding, 0)
	for _, outcome := range outcomes {
		report.Evaluations += outcome.evaluations
		pending = append(pending, outcome.findings...)
	}
	return pending
}

func appendFindings(report *Report, pending []pendingFinding) {
	for _, item := range pending {
		report.Findings = append(report.Findings, item.finding)
	}
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
	return runJobs(
		ctx,
		jobs,
		concurrency,
		func(ctx context.Context, job evaluationJob) (evaluationOutcome, error) {
			return evaluateJob(ctx, evaluator, job)
		},
	)
}

func runJobs[Job any, Outcome any](
	ctx context.Context,
	jobs []Job,
	concurrency int,
	evaluate func(context.Context, Job) (Outcome, error),
) ([]Outcome, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}

	jobContext, cancel := context.WithCancel(ctx)
	defer cancel()

	indices := queuedIndices(len(jobs))
	outcomes := make([]Outcome, len(jobs))
	firstError := runWorkers(jobContext, cancel, jobs, outcomes, indices, concurrency, evaluate)
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func queuedIndices(count int) <-chan int {
	indices := make(chan int, count)
	for index := range count {
		indices <- index
	}
	close(indices)
	return indices
}

func runWorkers[Job any, Outcome any](
	ctx context.Context,
	cancel context.CancelFunc,
	jobs []Job,
	outcomes []Outcome,
	indices <-chan int,
	concurrency int,
	evaluate func(context.Context, Job) (Outcome, error),
) error {
	var workers sync.WaitGroup
	var errorOnce sync.Once
	var firstError error

	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for index := range indices {
				if ctx.Err() != nil {
					return
				}
				outcome, err := evaluate(ctx, jobs[index])
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
	return firstError
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
	jobs := localizationJobs(findings)
	outcomes, err := runJobs(
		ctx,
		jobs,
		concurrency,
		func(ctx context.Context, job localizationJob) (localizationOutcome, error) {
			return evaluateLocalizationJob(ctx, evaluator, job)
		},
	)
	if err != nil {
		return 0, err
	}
	applyLocalizationOutcomes(findings, outcomes)
	return len(jobs), nil
}

func localizationJobs(findings []pendingFinding) []localizationJob {
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
	return jobs
}

func applyLocalizationOutcomes(
	findings []pendingFinding,
	outcomes []localizationOutcome,
) {
	for _, outcome := range outcomes {
		if outcome.location != nil {
			item := &findings[outcome.findingIndex]
			item.finding.Locations = append(item.finding.Locations, *outcome.location)
		}
	}
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
		if err := discoverRequestedPath(
			root,
			requestedPath,
			extractor,
			seen,
		); err != nil {
			return nil, err
		}
	}
	return sortedDiscoveredFiles(seen), nil
}

func discoverRequestedPath(
	root string,
	requestedPath string,
	extractor *parsing.Extractor,
	seen map[string]struct{},
) error {
	path := resolveRequestedPath(root, requestedPath)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", requestedPath, err)
	}
	if !info.IsDir() {
		addSupportedFile(path, extractor, seen)
		return nil
	}
	if err := walkSupportedFiles(path, extractor, seen); err != nil {
		return fmt.Errorf("walk %q: %w", requestedPath, err)
	}
	return nil
}

func resolveRequestedPath(root string, requestedPath string) string {
	if filepath.IsAbs(requestedPath) {
		return filepath.Clean(requestedPath)
	}
	return filepath.Clean(filepath.Join(root, requestedPath))
}

func walkSupportedFiles(
	root string,
	extractor *parsing.Extractor,
	seen map[string]struct{},
) error {
	return filepath.WalkDir(root, func(
		candidate string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		return collectWalkEntry(root, candidate, entry, walkErr, extractor, seen)
	})
}

func collectWalkEntry(
	root string,
	candidate string,
	entry fs.DirEntry,
	walkErr error,
	extractor *parsing.Extractor,
	seen map[string]struct{},
) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if candidate != root && ignoredDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.Type().IsRegular() {
		addSupportedFile(candidate, extractor, seen)
	}
	return nil
}

func addSupportedFile(
	path string,
	extractor *parsing.Extractor,
	seen map[string]struct{},
) {
	if extractor.Supports(path) {
		seen[path] = struct{}{}
	}
}

func sortedDiscoveredFiles(seen map[string]struct{}) []string {
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".venv", "node_modules", "vendor", "dist", "build":
		return true
	default:
		return false
	}
}
