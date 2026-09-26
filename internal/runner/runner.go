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

const defaultConcurrency = 4

const (
	codeUnitRankType = iota
	codeUnitRankFunction
	codeUnitRankComment
	codeUnitRankField
	codeUnitRankOther
)

type Runner struct {
	Extractor *parsing.Extractor
	Evaluator evaluation.Evaluator
}

type Options struct {
	Root          string
	Paths         []string
	Concurrency   int
	SourceOverlay map[string][]byte
}

type Report struct {
	ScannedFiles int                    `json:"scannedFiles"`
	CodeUnits    int                    `json:"codeUnits"`
	Evaluations  int                    `json:"evaluations"`
	Cache        *evaluation.CacheStats `json:"cache,omitempty"`
	Findings     []Finding              `json:"findings"`
	SourcePaths  []string               `json:"-"`
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
	root          string
	paths         []string
	concurrency   int
	sourceOverlay map[string][]byte
}

func (runner Runner) Evaluate(ctx context.Context, cfg config.Config, options Options) (Report, error) {
	cacheBefore, hasCacheStats := evaluation.CacheStats{}, false
	if provider, ok := runner.Evaluator.(evaluation.CacheStatsProvider); ok {
		cacheBefore, hasCacheStats = provider.CacheStats(), true
	}
	setup, err := runner.prepareCheck(options)
	if err != nil {
		return Report{}, err
	}
	files, err := discover(setup.root, setup.paths, runner.Extractor)
	if err != nil {
		return Report{}, err
	}
	report, jobs, err := runner.planEvaluations(
		ctx,
		cfg,
		setup.root,
		files,
		setup.sourceOverlay,
	)
	if err != nil {
		return Report{}, err
	}
	outcomes, err := runJobs(
		ctx,
		jobs,
		setup.concurrency,
		func(ctx context.Context, job evaluationJob) (evaluationOutcome, error) {
			return evaluateJob(ctx, runner.Evaluator, job)
		},
	)
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
	for _, item := range pending {
		report.Findings = append(report.Findings, item.finding)
	}
	if hasCacheStats {
		cacheAfter := evaluation.CacheStats{}
		if provider, ok := runner.Evaluator.(evaluation.CacheStatsProvider); ok {
			cacheAfter = provider.CacheStats()
		}
		cacheDelta := subtractCacheStats(cacheAfter, cacheBefore)
		if cacheDelta.Hits+cacheDelta.Misses+cacheDelta.Writes > 0 {
			report.Cache = &cacheDelta
		}
	}
	return report, nil
}

func subtractCacheStats(
	after evaluation.CacheStats,
	before evaluation.CacheStats,
) evaluation.CacheStats {
	return evaluation.CacheStats{
		Hits:   after.Hits - before.Hits,
		Misses: after.Misses - before.Misses,
		Writes: after.Writes - before.Writes,
	}
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
		concurrency = defaultConcurrency
	}

	root, err := filepath.Abs(options.Root)
	if err != nil {
		return checkSetup{}, fmt.Errorf("resolve project root: %w", err)
	}
	paths := options.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return checkSetup{
		root:          root,
		paths:         paths,
		concurrency:   concurrency,
		sourceOverlay: options.SourceOverlay,
	}, nil
}

func (runner Runner) planEvaluations(
	ctx context.Context,
	cfg config.Config,
	root string,
	files []string,
	sourceOverlay map[string][]byte,
) (Report, []evaluationJob, error) {
	report := Report{Findings: make([]Finding, 0)}
	jobs := make([]evaluationJob, 0)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Report{}, nil, err
		}
		fileJobs, codeUnits, scanned, err := runner.planFile(
			cfg,
			root,
			file,
			sourceOverlay,
		)
		if err != nil {
			return Report{}, nil, err
		}
		if scanned {
			report.ScannedFiles++
			relative, relErr := relativeProjectPath(root, file)
			if relErr != nil {
				return Report{}, nil, relErr
			}
			report.SourcePaths = append(report.SourcePaths, relative)
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
	sourceOverlay map[string][]byte,
) ([]evaluationJob, int, bool, error) {
	relative, err := relativeProjectPath(root, file)
	if err != nil {
		return nil, 0, false, err
	}
	applicable := make([]config.Rule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		applies, err := scoping.Applies(rule, relative)
		if err != nil {
			return nil, 0, false, err
		}
		if applies {
			applicable = append(applicable, rule)
		}
	}
	if len(applicable) == 0 {
		return nil, 0, false, nil
	}
	source, err := readOverlayOrFile(file, relative, sourceOverlay)
	if err != nil {
		return nil, 0, false, err
	}
	units, err := runner.Extractor.Extract(relative, source)
	if err != nil {
		return nil, 0, false, err
	}
	requested := requestedRegionKinds(applicable)
	if len(requested) > 0 {
		units = expandUnitsWithRegions(units, selectClosestRegions(units, requested))
	}
	return jobsForUnits(units, applicable), len(units), true, nil
}

func relativeProjectPath(root string, file string) (string, error) {
	relative, err := filepath.Rel(root, file)
	if err != nil {
		return "", fmt.Errorf(
			"make %q relative to project root: %w",
			file,
			err,
		)
	}
	return filepath.ToSlash(relative), nil
}

func readOverlayOrFile(
	file string,
	relative string,
	sourceOverlay map[string][]byte,
) ([]byte, error) {
	if source, ok := sourceOverlay[relative]; ok {
		return source, nil
	}
	source, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", relative, err)
	}
	return source, nil
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

type selectedRegion struct {
	unit       parsing.CodeUnit
	parentSpan uint
}

func selectClosestRegions(
	units []parsing.CodeUnit,
	requested map[parsing.CodeKind]parsing.CodeKind,
) []parsing.CodeUnit {
	selected := make(map[parsing.Region]selectedRegion)
	for _, parent := range units {
		parentSpan := parent.EndByte - parent.StartByte
		for _, region := range parent.Regions {
			rememberClosestRegion(selected, parent, region, parentSpan, requested)
		}
	}
	regions := make([]parsing.CodeUnit, 0, len(selected))
	for _, item := range selected {
		regions = append(regions, item.unit)
	}
	return regions
}

func rememberClosestRegion(
	selected map[parsing.Region]selectedRegion,
	parent parsing.CodeUnit,
	region parsing.Region,
	parentSpan uint,
	requested map[parsing.CodeKind]parsing.CodeKind,
) {
	kind, ok := requested[region.Category]
	if !ok {
		return
	}
	existing, exists := selected[region]
	if exists && existing.parentSpan <= parentSpan {
		return
	}
	selected[region] = selectedRegion{
		unit:       regionCodeUnit(parent, region, kind),
		parentSpan: parentSpan,
	}
}

func expandUnitsWithRegions(
	units []parsing.CodeUnit,
	regions []parsing.CodeUnit,
) []parsing.CodeUnit {
	expanded := append([]parsing.CodeUnit(nil), units...)
	expanded = append(expanded, regions...)
	sort.SliceStable(expanded, func(i, j int) bool {
		return codeUnitLess(expanded[i], expanded[j])
	})
	return expanded
}

func codeUnitLess(left parsing.CodeUnit, right parsing.CodeUnit) bool {
	if left.StartByte != right.StartByte {
		return left.StartByte < right.StartByte
	}
	rank := map[parsing.CodeKind]int{
		parsing.CodeKindType:     codeUnitRankType,
		parsing.CodeKindFunction: codeUnitRankFunction,
		parsing.CodeKindComment:  codeUnitRankComment,
		parsing.CodeKindField:    codeUnitRankField,
	}
	leftRank, rightRank := codeUnitRankOther, codeUnitRankOther
	if value, ok := rank[left.Kind]; ok {
		leftRank = value
	}
	if value, ok := rank[right.Kind]; ok {
		rightRank = value
	}
	return leftRank < rightRank
}

func requestedRegionKinds(rules []config.Rule) map[parsing.CodeKind]parsing.CodeKind {
	requested := make(map[parsing.CodeKind]parsing.CodeKind)
	for _, rule := range rules {
		for _, kind := range rule.Kinds {
			parsed, ok := parsing.ParseCodeKind(kind.String())
			if !ok {
				continue
			}
			switch parsed {
			case parsing.CodeKindComment, parsing.CodeKindField, parsing.CodeKindStatement:
				requested[parsed] = parsed
			}
		}
	}
	return requested
}

func regionCodeUnit(
	parent parsing.CodeUnit,
	region parsing.Region,
	kind parsing.CodeKind,
) parsing.CodeUnit {
	return parsing.CodeUnit{
		Kind:         kind,
		Name:         parent.Name + ":" + string(region.Kind),
		Language:     parent.Language,
		Path:         parent.Path,
		Source:       region.Source,
		ParentSource: parent.Source,
		RegionKind:   region.Kind,
		StartLine:    region.StartLine,
		EndLine:      region.EndLine,
		StartColumn:  region.StartColumn,
		EndColumn:    region.EndColumn,
		StartByte:    region.StartByte,
		EndByte:      region.EndByte,
		RelatedTypes: parent.RelatedTypes,
	}
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

func appliesToKind(rule config.Rule, kind parsing.CodeKind) bool {
	if len(rule.Kinds) == 0 {
		return kind == parsing.CodeKindFunction || kind == parsing.CodeKindType
	}
	for _, allowed := range rule.Kinds {
		parsed, ok := parsing.ParseCodeKind(allowed.String())
		if ok && parsed == kind {
			return true
		}
	}
	return false
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

	scheduled := make([]scheduledWork[Job, Outcome], len(jobs))
	indices := make(chan int, len(jobs))
	for index, job := range jobs {
		scheduled[index].job = job
		indices <- index
	}
	close(indices)
	firstError := runWorkers(jobContext, cancel, scheduled, indices, concurrency, evaluate)
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, len(scheduled))
	for index, item := range scheduled {
		outcomes[index] = item.outcome
	}
	return outcomes, nil
}

type scheduledWork[Job any, Outcome any] struct {
	job     Job
	outcome Outcome
}

func runWorkers[Job any, Outcome any](
	ctx context.Context,
	cancel context.CancelFunc,
	scheduled []scheduledWork[Job, Outcome],
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
				outcome, err := evaluate(ctx, scheduled[index].job)
				if err != nil {
					errorOnce.Do(func() {
						firstError = err
						cancel()
					})
					return
				}
				scheduled[index].outcome = outcome
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
				Language:    job.unit.Language.String(),
				Kind:        job.unit.Kind,
				Name:        job.unit.Name,
				StartLine:   job.unit.StartLine,
				EndLine:     job.unit.EndLine,
				StartColumn: job.unit.StartColumn,
				EndColumn:   job.unit.EndColumn,
				Snippet:     job.unit.Source,
				Locations:   directUnitLocations(job.unit),
			},
			rule: rule,
			unit: job.unit,
		})
	}
	return outcome, nil
}

func directUnitLocations(unit parsing.CodeUnit) []Location {
	if unit.RegionKind == "" {
		return nil
	}
	return []Location{{
		Category:    unit.Kind.String(),
		Kind:        string(unit.RegionKind),
		Source:      unit.Source,
		StartLine:   unit.StartLine,
		EndLine:     unit.EndLine,
		StartColumn: unit.StartColumn,
		EndColumn:   unit.EndColumn,
	}}
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
	for _, outcome := range outcomes {
		if outcome.location != nil {
			item := &findings[outcome.findingIndex]
			item.finding.Locations = append(item.finding.Locations, *outcome.location)
		}
	}
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

func evaluateLocalizationJob(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	job localizationJob,
) (localizationOutcome, error) {
	candidate := parsing.CodeUnit{
		Kind:         parsing.CodeKindRegion,
		Name:         job.parent.Name + ":" + string(job.region.Kind),
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
			Category:    job.region.Category.String(),
			Kind:        string(job.region.Kind),
			Source:      job.region.Source,
			StartLine:   job.region.StartLine,
			EndLine:     job.region.EndLine,
			StartColumn: job.region.StartColumn,
			EndColumn:   job.region.EndColumn,
		}
	}
	return outcome, nil
}

func localizesTo(rule config.Rule, category parsing.CodeKind) bool {
	for _, allowed := range rule.Localize {
		parsed, ok := parsing.ParseCodeKind(allowed.String())
		if ok && parsed == category {
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
		if extractor.Supports(path) {
			seen[path] = struct{}{}
		}
		return nil
	}
	if err := filepath.WalkDir(path, func(
		candidate string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		return collectWalkEntry(path, candidate, entry, walkErr, extractor, seen)
	}); err != nil {
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
		if candidate != root {
			if _, ignored := ignoredDirectoryNames[entry.Name()]; ignored {
				return filepath.SkipDir
			}
		}
		return nil
	}
	if entry.Type().IsRegular() && extractor.Supports(candidate) {
		seen[candidate] = struct{}{}
	}
	return nil
}

func sortedDiscoveredFiles(seen map[string]struct{}) []string {
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

var ignoredDirectoryNames = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".venv":        {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
}

