package runner

import (
	"context"
	"errors"
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

type booleanFieldEvaluator struct{}

type cacheStatsEvaluator struct {
	stats evaluation.CacheStats
}

type failingRecordingEvaluator struct {
	batches []evaluation.Batch
}

func (booleanFieldEvaluator) Evaluate(
	_ context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	status := evaluation.StatusPass
	if batch.CodeUnit.Kind == parsing.CodeKindType ||
		batch.CodeUnit.Kind == parsing.CodeKindRegion &&
			strings.HasPrefix(batch.CodeUnit.Source, "Flag") {
		status = evaluation.StatusFail
	}
	return map[string]evaluation.Result{
		"boolean-property-naming": {
			Status:     status,
			Confidence: 1,
		},
	}, nil
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

func (evaluator *cacheStatsEvaluator) Evaluate(
	_ context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	evaluator.stats.Misses++
	evaluator.stats.Writes++
	results := make(map[string]evaluation.Result, len(batch.Rules))
	for _, rule := range batch.Rules {
		results[rule.ID] = evaluation.Result{
			Status:     evaluation.StatusPass,
			Confidence: 1,
		}
	}
	return results, nil
}

func (evaluator *cacheStatsEvaluator) CacheStats() evaluation.CacheStats {
	return evaluator.stats
}

func (evaluator *failingRecordingEvaluator) Evaluate(
	_ context.Context,
	batch evaluation.Batch,
) (map[string]evaluation.Result, error) {
	evaluator.batches = append(evaluator.batches, batch)
	results := make(map[string]evaluation.Result, len(batch.Rules))
	for _, rule := range batch.Rules {
		results[rule.ID] = evaluation.Result{
			Status:     evaluation.StatusFail,
			Confidence: 1,
		}
	}
	return results, nil
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
		Extractor: testGoExtractor(t),
		Evaluator: evaluator,
	}).Evaluate(context.Background(), cfg, Options{Root: root, Concurrency: 1})
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
			Extractor: testGoExtractor(t),
			Evaluator: evaluator,
		}).Evaluate(context.Background(), cfg, Options{
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

func TestCheckReportsCacheStatsForCurrentRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "sample.go"),
		[]byte("package sample\n\nfunc Read() {}\n"),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	evaluator := &cacheStatsEvaluator{
		stats: evaluation.CacheStats{Hits: 10, Misses: 20, Writes: 15},
	}
	report, err := (Runner{
		Extractor: testGoExtractor(t),
		Evaluator: evaluator,
	}).Evaluate(context.Background(), config.Config{Rules: []config.Rule{{
		ID:          "rule",
		Description: "A rule.",
		Severity:    config.SeverityInfo,
		Kinds:       []config.TargetKind{config.TargetKindFunction},
	}}}, Options{Root: root, Concurrency: 1})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	want := evaluation.CacheStats{Misses: 1, Writes: 1}
	if report.Cache == nil || *report.Cache != want {
		t.Fatalf("cache stats = %#v, want %#v", report.Cache, want)
	}
}

func TestCheckEvaluatesRequestedRegionsDirectly(t *testing.T) {
	root := t.TempDir()
	source := "package sample\n\n" +
		"type Flags struct {\n" +
		"\t// Controls behavior.\n" +
		"\tFlag bool\n" +
		"}\n\n" +
		"func Read() {\n" +
		"\tprintln(\"read\")\n" +
		"}\n"
	if err := os.WriteFile(
		filepath.Join(root, "sample.go"),
		[]byte(source),
		0o600,
	); err != nil {
		t.Fatalf("write source: %v", err)
	}
	cfg := config.Config{Rules: []config.Rule{
		{
			ID:          "comments",
			Description: "Comments are useful.",
			Severity:    config.SeverityWarning,
			Kinds:       []config.TargetKind{config.TargetKindComment},
		},
		{
			ID:          "fields",
			Description: "Fields are clear.",
			Severity:    config.SeverityWarning,
			Kinds:       []config.TargetKind{config.TargetKindField},
		},
		{
			ID:          "statements",
			Description: "Statements are clear.",
			Severity:    config.SeverityWarning,
			Kinds:       []config.TargetKind{config.TargetKindStatement},
		},
	}}
	evaluator := &failingRecordingEvaluator{}
	report, err := (Runner{
		Extractor: testGoExtractor(t),
		Evaluator: evaluator,
	}).Evaluate(context.Background(), cfg, Options{Root: root, Concurrency: 1})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.CodeUnits != 5 {
		t.Fatalf("code units = %d, want two declarations and three regions", report.CodeUnits)
	}
	if report.Evaluations != 3 || len(evaluator.batches) != 3 {
		t.Fatalf(
			"evaluations = %d, batches = %d; want 3",
			report.Evaluations,
			len(evaluator.batches),
		)
	}
	wantKinds := []parsing.CodeKind{
		parsing.CodeKindComment,
		parsing.CodeKindField,
		parsing.CodeKindStatement,
	}
	for index, batch := range evaluator.batches {
		if batch.CodeUnit.Kind != wantKinds[index] {
			t.Fatalf(
				"batch %d kind = %q, want %q",
				index,
				batch.CodeUnit.Kind,
				wantKinds[index],
			)
		}
		if batch.CodeUnit.ParentSource == "" || batch.CodeUnit.RegionKind == "" {
			t.Fatalf("batch %d lacks region context: %#v", index, batch.CodeUnit)
		}
	}
	if len(report.Findings) != 3 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	for _, finding := range report.Findings {
		if len(finding.Locations) != 1 ||
			finding.Locations[0].Category != finding.Kind.String() ||
			finding.Locations[0].Kind == "" {
			t.Fatalf("direct finding location = %#v", finding)
		}
	}
}

func TestUnitsForRulesDeduplicatesRegionsUsingClosestParent(t *testing.T) {
	region := parsing.Region{
		Category:  parsing.CodeKindComment,
		Kind:      "comment",
		Source:    "// Helpful.",
		StartByte: 25,
		EndByte:   36,
	}
	units := []parsing.CodeUnit{
		{
			Kind:      parsing.CodeKindType,
			Name:      "Service",
			Source:    "type parent",
			StartByte: 0,
			EndByte:   100,
			Regions:   []parsing.Region{region},
		},
		{
			Kind:      parsing.CodeKindFunction,
			Name:      "Read",
			Source:    "function parent",
			StartByte: 20,
			EndByte:   50,
			Regions:   []parsing.Region{region},
		},
	}
	rules := []config.Rule{{Kinds: []config.TargetKind{config.TargetKindComment}}}

	expanded := expandUnitsForRules(units, rules)
	if len(expanded) != 3 {
		t.Fatalf("unitsForRules() = %#v, want one deduplicated region", expanded)
	}
	var comment *parsing.CodeUnit
	for index := range expanded {
		if expanded[index].Kind == parsing.CodeKindComment {
			comment = &expanded[index]
		}
	}
	if comment == nil || comment.ParentSource != "function parent" {
		t.Fatalf("comment unit = %#v, want closest function parent", comment)
	}

	if got := expandUnitsForRules(units, []config.Rule{{}}); len(got) != len(units) {
		t.Fatalf("omitted kinds expanded %d units, want %d", len(got), len(units))
	}
}

func expandUnitsForRules(
	units []parsing.CodeUnit,
	rules []config.Rule,
) []parsing.CodeUnit {
	requested := requestedRegionKinds(rules)
	if len(requested) == 0 {
		return units
	}
	return expandUnitsWithRegions(units, selectClosestRegions(units, requested))
}

func TestRunJobsPreservesInputOrder(t *testing.T) {
	releases := []chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
		make(chan struct{}),
	}
	started := make(chan struct{}, len(releases))

	type result struct {
		outcomes []int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		outcomes, err := runJobs(
			context.Background(),
			[]int{0, 1, 2},
			3,
			func(_ context.Context, job int) (int, error) {
				started <- struct{}{}
				<-releases[job]
				return job, nil
			},
		)
		done <- result{outcomes: outcomes, err: err}
	}()

	for range releases {
		<-started
	}
	close(releases[2])
	close(releases[1])
	close(releases[0])

	got := <-done
	if got.err != nil {
		t.Fatalf("runJobs() error = %v", got.err)
	}
	for index, outcome := range got.outcomes {
		if outcome != index {
			t.Fatalf("runJobs() outcomes = %v, want [0 1 2]", got.outcomes)
		}
	}
}

func TestRunJobsCancelsPeersAfterError(t *testing.T) {
	sentinel := errors.New("evaluation failed")
	peerStarted := make(chan struct{})

	_, err := runJobs(
		context.Background(),
		[]int{0, 1},
		2,
		func(ctx context.Context, job int) (int, error) {
			if job == 0 {
				<-peerStarted
				return 0, sentinel
			}
			close(peerStarted)
			<-ctx.Done()
			return 0, ctx.Err()
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("runJobs() error = %v, want %v", err, sentinel)
	}
}

func TestCheckLocalizesFailedRuleToTreeSitterRegion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := "package sample\n\n// FeatureFlags controls behavior.\n" +
		"type FeatureFlags struct {\n\tFlag bool\n\tIsReady bool\n}\n\n" +
		"func ReadFlags() {}\n"
	if err := os.WriteFile(filepath.Join(root, "flags.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := config.Config{Rules: []config.Rule{{
		ID:          "boolean-property-naming",
		Description: "Boolean fields clearly describe the true state.",
		Severity:    config.SeverityWarning,
		Kinds:       []config.TargetKind{config.TargetKindType},
		Localize:    []config.TargetKind{config.TargetKindField},
	}}}
	report, err := (Runner{
		Extractor: testGoExtractor(t),
		Evaluator: booleanFieldEvaluator{},
	}).Evaluate(context.Background(), cfg, Options{Root: root, Concurrency: 2})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.Evaluations != 3 {
		t.Fatalf("evaluations = %d, want initial type plus two fields", report.Evaluations)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	locations := report.Findings[0].Locations
	if len(locations) != 1 {
		t.Fatalf("locations = %#v", locations)
	}
	if locations[0].Source != "Flag bool" ||
		locations[0].Category != "field" ||
		locations[0].StartLine != 5 ||
		locations[0].StartColumn != 1 {
		t.Fatalf("location = %#v", locations[0])
	}
}

func TestLocalizesToRequiresExplicitCategory(t *testing.T) {
	t.Parallel()

	if localizesTo(config.Rule{}, parsing.CodeKindStatement) {
		t.Fatal("omitted localization should skip the second pass")
	}
	if localizesTo(config.Rule{Localize: []config.TargetKind{}}, parsing.CodeKindStatement) {
		t.Fatal("empty localization should skip the second pass")
	}
	if !localizesTo(
		config.Rule{Localize: []config.TargetKind{config.TargetKindStatement}},
		parsing.CodeKindStatement,
	) {
		t.Fatal("explicit statement localization should run the second pass")
	}
}

func TestCheckUsesSourceOverlayAndReportsScannedPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	diskSource := "package sample\n\nfunc DiskName() {}\n"
	if err := os.WriteFile(
		filepath.Join(root, "sample.go"),
		[]byte(diskSource),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	evaluator := &failingRecordingEvaluator{}
	report, err := (Runner{
		Extractor: testGoExtractor(t),
		Evaluator: evaluator,
	}).Evaluate(context.Background(), config.Config{Rules: []config.Rule{{
		ID:          "names",
		Description: "Use an overlay name.",
		Severity:    config.SeverityError,
		Localize:    []config.TargetKind{},
	}}}, Options{
		Root: root,
		SourceOverlay: map[string][]byte{
			"sample.go": []byte("package sample\n\nfunc OverlayName() {}\n"),
		},
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(evaluator.batches) != 1 ||
		evaluator.batches[0].CodeUnit.Name != "OverlayName" {
		t.Fatalf("Check() batches = %#v", evaluator.batches)
	}
	if len(report.SourcePaths) != 1 || report.SourcePaths[0] != "sample.go" {
		t.Fatalf("Check() source paths = %#v", report.SourcePaths)
	}
	content, err := os.ReadFile(filepath.Join(root, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != diskSource {
		t.Fatalf("Check() modified disk source: %q", content)
	}
}

func TestDiscoverFiltersDeduplicatesAndSortsFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := map[string]string{
		"z.go":                         "package sample\n",
		"nested/a.ts":                  "export function read() {}\n",
		"nested/notes.txt":             "not source\n",
		"node_modules/ignored.go":      "package ignored\n",
		"nested/build/generated.py":    "def generated(): pass\n",
		"nested/vendor/dependency.rs":  "fn dependency() {}\n",
		"nested/.git/internal_file.go": "package hidden\n",
	}
	for path, contents := range files {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatalf("create parent for %q: %v", path, err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}

	discovered, err := discover(
		root,
		[]string{".", "z.go", filepath.Join(root, "nested"), "nested/notes.txt"},
		testExtractor(t, "go", "typescript"),
	)
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	want := []string{
		filepath.Join(root, "nested/a.ts"),
		filepath.Join(root, "z.go"),
	}
	if strings.Join(discovered, "\n") != strings.Join(want, "\n") {
		t.Fatalf("discover() files = %q, want %q", discovered, want)
	}
}

func TestDiscoverRejectsMissingPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := discover(root, []string{"missing"}, testGoExtractor(t))
	if err == nil || !strings.Contains(err.Error(), `inspect "missing"`) {
		t.Fatalf("discover() error = %v", err)
	}
}

func testGoExtractor(t *testing.T) *parsing.Extractor {
	t.Helper()

	return testExtractor(t, "go")
}

func testExtractor(t *testing.T, presets ...string) *parsing.Extractor {
	t.Helper()

	languages := make(map[string]config.Language, len(presets))
	for _, preset := range presets {
		languages[preset] = config.Language{}
	}
	extractor, err := parsing.NewExtractor(languages)
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}
	return extractor
}
