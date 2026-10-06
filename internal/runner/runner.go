package runner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/evaluation"
	"github.com/codegirl-007/jevlint/internal/parsing"
	"github.com/codegirl-007/jevlint/internal/scoping"
)

const defaultConcurrency = 4

const (
	codeUnitRankType = iota
	codeUnitRankFunction
	codeUnitRankComment
	codeUnitRankField
	codeUnitRankOther
)

// Runner reads code and asks the evaluator about it.
type Runner struct {
	Extractor *parsing.Extractor
	Evaluator evaluation.Evaluator
}

// Options holds the settings for a run.
type Options struct {
	Root          string
	Paths         []string
	Concurrency   int
	SourceOverlay map[string][]byte
}

// Report is the outcome of a run.
type Report struct {
	ScannedFiles int                    `json:"scannedFiles"`
	CodeUnits    int                    `json:"codeUnits"`
	Evaluations  int                    `json:"evaluations"`
	Cache        *evaluation.CacheStats `json:"cache,omitempty"`
	Findings     []Finding              `json:"findings"`
	SourcePaths  []string               `json:"-"`
}

// Finding is one rule failure for one piece of code.
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

// Location is a smaller place inside a finding.
type Location struct {
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	StartLine   uint   `json:"startLine"`
	EndLine     uint   `json:"endLine"`
	StartColumn uint   `json:"startColumn"`
	EndColumn   uint   `json:"endColumn"`
}

// evaluationJob is one piece of code and the rules to check against it.
type evaluationJob struct {
	rules []checkRule
	unit  parsing.CodeUnit
}

// checkRule is one rule with its config target kinds resolved once for the run.
type checkRule struct {
	rule     config.Rule
	kinds    map[parsing.CodeKind]struct{}
	localize map[parsing.CodeKind]struct{}
}

// compileRules resolves each rule's config kinds once, at the run boundary.
func compileRules(rules []config.Rule) []checkRule {
	compiled := make([]checkRule, 0, len(rules))
	for _, rule := range rules {
		compiled = append(compiled, checkRule{
			rule:     rule,
			kinds:    resolveKinds(rule.Kinds),
			localize: resolveKinds(rule.Localize),
		})
	}
	return compiled
}

// resolveKinds turns config target kinds into parsed code kinds.
func resolveKinds(kinds []config.TargetKind) map[parsing.CodeKind]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	resolved := make(map[parsing.CodeKind]struct{}, len(kinds))
	for _, kind := range kinds {
		parsed, err := parsing.ParseCodeKind(kind.String())
		if err != nil {
			continue
		}
		resolved[parsed] = struct{}{}
	}
	return resolved
}

// appliesTo reports whether the rule checks a given kind.
func (rule checkRule) appliesTo(kind parsing.CodeKind) bool {
	if len(rule.kinds) == 0 {
		return kind == parsing.CodeKindFunction || kind == parsing.CodeKindType
	}
	_, ok := rule.kinds[kind]
	return ok
}

// localizesTo reports whether the rule can point at a given kind of region.
func (rule checkRule) localizesTo(category parsing.CodeKind) bool {
	_, ok := rule.localize[category]
	return ok
}

// configRules returns the underlying config rules for evaluation.
func configRules(rules []checkRule) []config.Rule {
	out := make([]config.Rule, len(rules))
	for index, rule := range rules {
		out[index] = rule.rule
	}
	return out
}

// evaluationOutcome is the result of one job.
type evaluationOutcome struct {
	evaluations int
	findings    []pendingFinding
}

// pendingFinding is a finding that may still gain a location.
type pendingFinding struct {
	finding Finding
	rule    checkRule
	unit    parsing.CodeUnit
}

// localizationJob is one attempt to point at the exact place that failed.
type localizationJob struct {
	findingIndex int
	rule         checkRule
	parent       parsing.CodeUnit
	region       parsing.Region
}

// localizationOutcome is the place found for one finding.
type localizationOutcome struct {
	findingIndex int
	location     *Location
}

// checkSetup holds the resolved settings for a run.
type checkSetup struct {
	root          string
	paths         []string
	concurrency   int
	sourceOverlay map[string][]byte
}

// plannedFile is a source file and the work it produced.
type plannedFile struct {
	relative   string
	units      []parsing.CodeUnit
	applicable []checkRule
}

// selectedRegion is a region and the size of the unit that holds it.
type selectedRegion struct {
	unit       parsing.CodeUnit
	parentSpan uint
}

// scheduledWork pairs a job with its outcome.
type scheduledWork[Job any, Outcome any] struct {
	job     Job
	outcome Outcome
}

// Evaluate reads the files and returns the findings.
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
			return evaluateJob(ctx, runner.Evaluator, job, cfg)
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
		cfg,
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

// subtractCacheStats returns the cache counts added during a run.
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

// prepareCheck checks the settings and resolves the project root.
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

// planEvaluations reads the files and builds the work for each one.
func (runner Runner) planEvaluations(
	ctx context.Context,
	cfg config.Config,
	root string,
	files []string,
	sourceOverlay map[string][]byte,
) (Report, []evaluationJob, error) {
	compiled := compileRules(cfg.Rules)
	needCallees := rulesWantCallees(compiled)
	extracted := make([]plannedFile, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Report{}, nil, err
		}
		planned, err := runner.extractFile(
			compiled,
			root,
			file,
			sourceOverlay,
			needCallees,
		)
		if err != nil {
			return Report{}, nil, err
		}
		if planned.relative == "" {
			continue
		}
		extracted = append(extracted, planned)
	}
	if needCallees {
		parsing.ResolveCallees(functionUnits(extracted))
	}

	report := Report{Findings: make([]Finding, 0)}
	jobs := make([]evaluationJob, 0)
	for _, planned := range extracted {
		if len(planned.applicable) == 0 {
			continue
		}
		report.ScannedFiles++
		report.SourcePaths = append(report.SourcePaths, planned.relative)
		report.CodeUnits += len(planned.units)
		jobs = append(jobs, jobsForUnits(planned.units, planned.applicable)...)
	}
	return report, jobs, nil
}

// extractFile reads one file and records the rules that apply to it.
func (runner Runner) extractFile(
	rules []checkRule,
	root string,
	file string,
	sourceOverlay map[string][]byte,
	needCallees bool,
) (plannedFile, error) {
	relative, err := relativeProjectPath(root, file)
	if err != nil {
		return plannedFile{}, err
	}
	applicable := make([]checkRule, 0, len(rules))
	for _, rule := range rules {
		applies, err := scoping.Applies(rule.rule, relative)
		if err != nil {
			return plannedFile{}, err
		}
		if applies {
			applicable = append(applicable, rule)
		}
	}
	if len(applicable) == 0 && !needCallees {
		return plannedFile{}, nil
	}
	source, err := readOverlayOrFile(file, relative, sourceOverlay)
	if err != nil {
		if len(applicable) == 0 {
			return plannedFile{}, nil
		}
		return plannedFile{}, err
	}
	units, err := runner.Extractor.Extract(relative, source)
	if err != nil {
		if len(applicable) == 0 {
			return plannedFile{}, nil
		}
		return plannedFile{}, err
	}
	if len(applicable) > 0 {
		requested := requestedRegionKinds(applicable)
		if len(requested) > 0 {
			units = expandUnitsWithRegions(units, selectClosestRegions(units, requested))
		}
	}
	return plannedFile{
		relative:   relative,
		units:      units,
		applicable: applicable,
	}, nil
}

// functionUnits collects the function units from the planned files.
func functionUnits(files []plannedFile) []*parsing.CodeUnit {
	functions := make([]*parsing.CodeUnit, 0)
	for fileIndex := range files {
		for unitIndex := range files[fileIndex].units {
			unit := &files[fileIndex].units[unitIndex]
			if unit.Kind == parsing.CodeKindFunction {
				functions = append(functions, unit)
			}
		}
	}
	return functions
}

// rulesWantCallees reports whether any rule asks for the called functions.
func rulesWantCallees(rules []checkRule) bool {
	for _, rule := range rules {
		if rule.rule.Context.Callees {
			return true
		}
	}
	return false
}

// relativeProjectPath returns a path relative to the project root.
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

// readOverlayOrFile reads a file from the given source or from disk.
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

// jobsForUnits builds the work for each code unit and its rules.
func jobsForUnits(units []parsing.CodeUnit, rules []checkRule) []evaluationJob {
	jobs := make([]evaluationJob, 0, len(units))
	for _, unit := range units {
		ordinary := make([]checkRule, 0, len(rules))
		enriched := make([]checkRule, 0, len(rules))
		for _, rule := range rules {
			if !rule.appliesTo(unit.Kind) {
				continue
			}
			if rule.rule.Context.Callees {
				enriched = append(enriched, rule)
				continue
			}
			ordinary = append(ordinary, rule)
		}
		if len(ordinary) > 0 {
			jobs = append(jobs, evaluationJob{rules: ordinary, unit: unit})
		}
		if len(enriched) > 0 {
			jobs = append(jobs, evaluationJob{rules: enriched, unit: unit})
		}
	}
	return jobs
}

// selectClosestRegions picks each wanted region once, from its closest parent.
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

// rememberClosestRegion keeps a region when it has a closer parent.
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

// expandUnitsWithRegions adds the wanted regions to the list of units.
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

// codeUnitLess orders code units that start at the same place.
func codeUnitLess(left parsing.CodeUnit, right parsing.CodeUnit) bool {
	if left.StartByte != right.StartByte {
		return left.StartByte < right.StartByte
	}
	rank := map[parsing.CodeKind]int{
		parsing.CodeKindType:       codeUnitRankType,
		parsing.CodeKindFunction:   codeUnitRankFunction,
		parsing.CodeKindComment:    codeUnitRankComment,
		parsing.CodeKindDocComment: codeUnitRankComment,
		parsing.CodeKindField:      codeUnitRankField,
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

// requestedRegionKinds returns the region kinds the rules ask for.
func requestedRegionKinds(rules []checkRule) map[parsing.CodeKind]parsing.CodeKind {
	requested := make(map[parsing.CodeKind]parsing.CodeKind)
	for _, rule := range rules {
		for kind := range rule.kinds {
			switch kind {
			case parsing.CodeKindComment, parsing.CodeKindDocComment,
				parsing.CodeKindField, parsing.CodeKindStatement:
				requested[kind] = kind
			}
		}
	}
	return requested
}

// regionCodeUnit builds a code unit for a region inside a parent.
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
		UnitContext:  parsing.UnitContext{RelatedTypes: parent.RelatedTypes},
	}
}

// collectOutcomes gathers the findings and counts from the jobs.
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

// runJobs runs the jobs with the given number of workers and keeps their order.
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

// runWorkers runs the jobs and stops the others when one fails.
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

// evaluateJob asks the evaluator about one job and builds its findings.
func evaluateJob(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	job evaluationJob,
	cfg config.Config,
) (evaluationOutcome, error) {
	results, err := evaluator.Evaluate(ctx, evaluation.Batch{
		Rules:    configRules(job.rules),
		CodeUnit: requestUnit(job),
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
		result, ok := results[rule.rule.ID]
		if !ok {
			return evaluationOutcome{}, fmt.Errorf(
				"invalid result for rule %q at %s:%d: result is missing",
				rule.rule.ID,
				job.unit.Path,
				job.unit.StartLine,
			)
		}
		if err := result.Validate(); err != nil {
			return evaluationOutcome{}, fmt.Errorf(
				"invalid result for rule %q at %s:%d: %w",
				rule.rule.ID,
				job.unit.Path,
				job.unit.StartLine,
				err,
			)
		}
		outcome.evaluations++

		if result.Status != evaluation.StatusFail ||
			result.Confidence < cfg.ConfidenceFloor(rule.rule) {
			continue
		}
		outcome.findings = append(outcome.findings, pendingFinding{
			finding: Finding{
				RuleID:      rule.rule.ID,
				Description: rule.rule.Description,
				Severity:    rule.rule.Severity,
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

// directUnitLocations returns the place of a region that is checked on its own.
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

// requestUnit adds the called functions when the rules ask for them.
func requestUnit(job evaluationJob) parsing.CodeUnit {
	if rulesWantCallees(job.rules) {
		return parsing.WithCalleeContext(job.unit)
	}
	return job.unit
}

// localizeFindings points at the exact places inside failed units.
func localizeFindings(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	findings []pendingFinding,
	concurrency int,
	cfg config.Config,
) (int, error) {
	jobs := localizationJobs(findings)
	outcomes, err := runJobs(
		ctx,
		jobs,
		concurrency,
		func(ctx context.Context, job localizationJob) (localizationOutcome, error) {
			return evaluateLocalizationJob(ctx, evaluator, job, cfg)
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

// localizationJobs builds one job for each place a rule wants to point at.
func localizationJobs(findings []pendingFinding) []localizationJob {
	jobs := make([]localizationJob, 0)
	for findingIndex, item := range findings {
		for _, region := range item.unit.Regions {
			if !item.rule.localizesTo(region.Category) {
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

// evaluateLocalizationJob checks one place and returns it when it failed.
func evaluateLocalizationJob(
	ctx context.Context,
	evaluator evaluation.Evaluator,
	job localizationJob,
	cfg config.Config,
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
		UnitContext:  parsing.UnitContext{RelatedTypes: job.parent.RelatedTypes},
	}
	if job.rule.rule.Context.Callees {
		candidate.Callees = parsing.ExpandCallees(job.parent.Resolved)
	}
	results, err := evaluator.Evaluate(ctx, evaluation.Batch{
		Rules:    []config.Rule{job.rule.rule},
		CodeUnit: candidate,
	})
	if err != nil {
		return localizationOutcome{}, fmt.Errorf(
			"localize rule %q at %s:%d: %w",
			job.rule.rule.ID,
			job.parent.Path,
			job.region.StartLine,
			err,
		)
	}
	result, ok := results[job.rule.rule.ID]
	if !ok {
		return localizationOutcome{}, fmt.Errorf(
			"invalid localization result for rule %q at %s:%d: result is missing",
			job.rule.rule.ID,
			job.parent.Path,
			job.region.StartLine,
		)
	}
	if err := result.Validate(); err != nil {
		return localizationOutcome{}, fmt.Errorf(
			"invalid localization result for rule %q at %s:%d: %w",
			job.rule.rule.ID,
			job.parent.Path,
			job.region.StartLine,
			err,
		)
	}

	outcome := localizationOutcome{findingIndex: job.findingIndex}
	if result.Status == evaluation.StatusFail &&
		result.Confidence >= cfg.ConfidenceFloor(job.rule.rule) {
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

// HasFailures reports whether the report has any failures.
func (report Report) HasFailures() bool {
	for _, finding := range report.Findings {
		if finding.Status == evaluation.StatusFail {
			return true
		}
	}
	return false
}

// discover lists the supported files under the requested paths.
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

// discoverRequestedPath lists the supported files under one path.
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

// resolveRequestedPath turns a requested path into a full path.
func resolveRequestedPath(root string, requestedPath string) string {
	if filepath.IsAbs(requestedPath) {
		return filepath.Clean(requestedPath)
	}
	return filepath.Clean(filepath.Join(root, requestedPath))
}

// collectWalkEntry adds a file from a directory walk and skips ignored folders.
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

// sortedDiscoveredFiles returns the found files in a stable order.
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
