package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/fix"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const usage = `Usage:
  jevlint check [flags] [paths...]
  jevlint fix [flags] [paths...]

Flags:
  --clear-cache         clear this project's cached evaluations before checking
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default "jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --format text|json    output format (default "text")
  --no-cache            bypass evaluation cache reads and writes
  --refresh-cache       reevaluate and replace current cached results
  --fix                 print findings, then apply a validated fix
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	command := args[0]
	if command == "mcp-check" {
		if err := fix.ServeCheck(ctx, os.Stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "jevlint: mcp-check: %v\n", err)
			return 2
		}
		return 0
	}
	if command == "-h" || command == "--help" {
		fmt.Fprint(stderr, usage)
		return 0
	}
	if command != "check" && command != "fix" {
		fmt.Fprintf(stderr, "jevlint: unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	clearCache := flags.Bool("clear-cache", false, "clear cached evaluations")
	color := flags.String("color", "auto", "color output")
	configPath := flags.String("config", "jevlint.json", "rule configuration")
	concurrency := flags.Int("concurrency", 4, "maximum concurrent Jev requests")
	format := flags.String("format", "text", "output format")
	noCache := flags.Bool("no-cache", false, "bypass evaluation cache")
	refreshCache := flags.Bool("refresh-cache", false, "refresh cached evaluations")
	autoFix := flags.Bool("fix", false, "print findings and apply a validated fix")
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
	}
	flagArgs, paths, err := splitFlagsAndPaths(flags, args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "jevlint: --format must be text or json\n")
		return 2
	}
	if *color != "auto" && *color != "always" && *color != "never" {
		fmt.Fprintln(stderr, "jevlint: --color must be auto, always, or never")
		return 2
	}
	if *concurrency < 1 {
		fmt.Fprintln(stderr, "jevlint: --concurrency must be at least 1")
		return 2
	}
	bypassCache := *noCache || *autoFix
	if bypassCache && *refreshCache {
		if *autoFix && !*noCache {
			fmt.Fprintln(stderr, "jevlint: --fix and --refresh-cache cannot be combined")
		} else {
			fmt.Fprintln(stderr, "jevlint: --no-cache and --refresh-cache cannot be combined")
		}
		return 2
	}

	absoluteConfig, err := filepath.Abs(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return 2
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}

	extractor, err := parsing.NewExtractor(cfg.Languages)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: configure languages: %v\n", err)
		return 2
	}

	projectRoot := filepath.Dir(absoluteConfig)
	var resultCache evaluation.ResultCache
	if !bypassCache || *clearCache {
		cache, cacheErr := evaluation.NewFileCache(projectRoot)
		if cacheErr != nil {
			if *clearCache {
				fmt.Fprintf(stderr, "jevlint: %v\n", cacheErr)
				return 2
			}
			fmt.Fprintf(stderr, "jevlint: cache disabled: %v\n", cacheErr)
		} else {
			if *clearCache {
				if err := cache.Clear(); err != nil {
					fmt.Fprintf(stderr, "jevlint: %v\n", err)
					return 2
				}
			}
			if !bypassCache {
				resultCache = cache
			}
		}
	}

	evaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{
			Cache:   resultCache,
			Refresh: *refreshCache && !bypassCache,
		},
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}

	checker := runner.Runner{
		Extractor: extractor,
		Evaluator: evaluator,
	}
	if command == "fix" || *autoFix {
		newFixProgress(
			stderr,
			shouldUseColor(*color, stderr),
		).status("checking for findings")
	}
	report, err := checker.Check(ctx, cfg, runner.Options{
		Root:        projectRoot,
		Paths:       paths,
		Concurrency: *concurrency,
	})
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}

	if *autoFix {
		if *format == "text" || !report.HasFailures() {
			if err := writeReport(stdout, report, *format, *color); err != nil {
				fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
				return 2
			}
		}
		if !report.HasFailures() {
			return 0
		}
		return runFix(
			ctx,
			stdout,
			stderr,
			cfg,
			absoluteConfig,
			extractor,
			report,
			*concurrency,
			*format,
			*color,
		)
	}
	if command == "fix" {
		return runFix(
			ctx,
			stdout,
			stderr,
			cfg,
			absoluteConfig,
			extractor,
			report,
			*concurrency,
			*format,
			*color,
		)
	}
	if err := writeReport(stdout, report, *format, *color); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return 2
	}

	if report.HasFailures() {
		return 1
	}
	return 0
}

type fixOutput struct {
	Validated     bool             `json:"validated"`
	Applied       bool             `json:"applied"`
	ModifiedFiles []string         `json:"modifiedFiles"`
	Findings      []runner.Finding `json:"findings"`
	Diff          string           `json:"diff,omitempty"`
}

type rejectedFixError struct {
	message string
}

func (fixError rejectedFixError) Error() string {
	return fixError.message
}

func runFix(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	cfg config.Config,
	configPath string,
	extractor *parsing.Extractor,
	report runner.Report,
	concurrency int,
	format string,
	color string,
) int {
	if cfg.Fix == nil {
		fmt.Fprintln(stderr, "jevlint: fix.command is required for the fix command")
		return 2
	}
	if !report.HasFailures() {
		if err := writeReport(stdout, report, format, color); err != nil {
			fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
			return 2
		}
		return 0
	}

	progress := newFixProgress(stderr, shouldUseColor(color, stderr))
	proposal, err := prepareFixProposal(
		ctx,
		cfg,
		configPath,
		extractor,
		report,
		progress.status,
		progress.agentMessage,
		progress.toolActivity,
	)
	progress.finishAgentMessage()
	if err != nil {
		return reportFixPreparationError(stderr, err)
	}
	progress.status("validating proposed changes with Jev")
	validation, err := validateFixProposal(
		ctx,
		cfg,
		filepath.Dir(configPath),
		extractor,
		report.Findings,
		proposal,
		concurrency,
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: validate fix: %v\n", err)
		return 2
	}
	return finishFix(
		stdout,
		stderr,
		proposal,
		validation,
		format,
		color,
		filepath.Dir(configPath),
	)
}

func prepareFixProposal(
	ctx context.Context,
	cfg config.Config,
	configPath string,
	extractor *parsing.Extractor,
	report runner.Report,
	progress func(string),
	agentMessage func(string),
	toolActivity func(string),
) (fix.Proposal, error) {
	proposal, err := fix.Generate(ctx, fix.Options{
		Root:         filepath.Dir(configPath),
		ConfigPath:   configPath,
		Command:      cfg.Fix.Command,
		Context:      cfg.Fix.Context,
		Exclude:      cfg.Fix.Exclude,
		Findings:     report.Findings,
		Progress:     progress,
		AgentMessage: agentMessage,
		ToolActivity: toolActivity,
	})
	if err != nil {
		return fix.Proposal{}, err
	}
	if len(proposal.Changes) == 0 {
		return fix.Proposal{}, rejectedFixError{
			message: "ACP agent made no source changes",
		}
	}
	if err := fix.ValidateSyntax(extractor, proposal.Changes); err != nil {
		if restoreErr := fix.RestoreChanges(
			filepath.Dir(configPath),
			proposal.Changes,
		); restoreErr != nil {
			return fix.Proposal{}, fmt.Errorf(
				"proposed fix has invalid syntax: %w (restore failed: %v)",
				err,
				restoreErr,
			)
		}
		return fix.Proposal{}, rejectedFixError{
			message: "proposed fix has invalid syntax: " + err.Error(),
		}
	}
	return proposal, nil
}

func reportFixPreparationError(stderr io.Writer, err error) int {
	var rejection rejectedFixError
	if errors.As(err, &rejection) {
		fmt.Fprintf(stderr, "jevlint: %s\n", rejection.message)
		return 1
	}
	fmt.Fprintf(stderr, "jevlint: generate fix: %v\n", err)
	return 2
}

func validateFixProposal(
	ctx context.Context,
	cfg config.Config,
	projectRoot string,
	extractor *parsing.Extractor,
	findings []runner.Finding,
	proposal fix.Proposal,
	concurrency int,
) (runner.Report, error) {
	overlay := make(map[string][]byte, len(proposal.Changes))
	for _, change := range proposal.Changes {
		overlay[change.Path] = change.After
	}
	validationEvaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{},
	)
	if err != nil {
		return runner.Report{}, fmt.Errorf(
			"configure evaluator: %w",
			err,
		)
	}
	return (runner.Runner{
		Extractor: extractor,
		Evaluator: validationEvaluator,
	}).Check(ctx, cfg, runner.Options{
		Root:          projectRoot,
		Paths:         findingPaths(findings),
		Concurrency:   concurrency,
		SourceOverlay: overlay,
	})
}

func finishFix(
	stdout io.Writer,
	stderr io.Writer,
	proposal fix.Proposal,
	validation runner.Report,
	format string,
	color string,
	projectRoot string,
) int {
	if validation.HasFailures() {
		if err := fix.RestoreChanges(projectRoot, proposal.Changes); err != nil {
			fmt.Fprintf(stderr, "jevlint: restore rejected fix: %v\n", err)
			return 2
		}
		return writeRejectedFix(
			stdout,
			stderr,
			proposal,
			validation,
			format,
			color,
		)
	}
	if err := writeFixOutput(
		stdout,
		fixOutput{
			Validated:     true,
			Applied:       true,
			ModifiedFiles: proposalPaths(proposal),
			Findings:      []runner.Finding{},
		},
		format,
		color,
	); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return 2
	}
	return 0
}

func writeRejectedFix(
	stdout io.Writer,
	stderr io.Writer,
	proposal fix.Proposal,
	validation runner.Report,
	format string,
	color string,
) int {
	if err := writeFixOutput(
		stdout,
		fixOutput{
			ModifiedFiles: proposalPaths(proposal),
			Findings:      validation.Findings,
		},
		format,
		color,
	); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return 2
	}
	fmt.Fprintln(stderr, "jevlint: proposed fix did not resolve every finding")
	return 1
}

func findingPaths(findings []runner.Finding) []string {
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, finding := range findings {
		if _, ok := seen[finding.Path]; ok {
			continue
		}
		seen[finding.Path] = struct{}{}
		paths = append(paths, finding.Path)
	}
	return paths
}

func proposalPaths(proposal fix.Proposal) []string {
	paths := make([]string, 0, len(proposal.Changes))
	for _, change := range proposal.Changes {
		paths = append(paths, change.Path)
	}
	return paths
}

func writeFixOutput(
	writer io.Writer,
	output fixOutput,
	format string,
	color string,
) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	style := outputStyle{color: shouldUseColor(color, writer)}
	if !output.Validated {
		fmt.Fprintf(
			writer,
			"\n  %s %s\n\n",
			style.paint("1;33", "!"),
			style.paint("1;33", "Proposed changes (rejected)"),
		)
		writeModifiedFiles(writer, style, output.ModifiedFiles)
		fmt.Fprintf(writer, "%s\n", style.paint("1", "Validation feedback"))
		for _, finding := range output.Findings {
			writeFinding(writer, style, finding)
		}
		writeSummary(writer, style, output.Findings)
		return nil
	}
	heading := "Validated proposed changes"
	if output.Applied {
		heading = "Applied proposed changes"
	}
	fmt.Fprintf(
		writer,
		"\n  %s %s\n\n",
		style.paint("1;32", "✓"),
		style.paint("1;32", heading),
	)
	writeModifiedFiles(writer, style, output.ModifiedFiles)
	return nil
}

func writeModifiedFiles(writer io.Writer, style outputStyle, paths []string) {
	fmt.Fprintln(writer, style.paint("1", "Modified files"))
	for _, path := range paths {
		fmt.Fprintf(
			writer,
			"  %s %s\n",
			style.paint("33", "M"),
			style.paint("36", path),
		)
	}
	fmt.Fprintln(writer)
}

func writeReport(
	writer io.Writer,
	report runner.Report,
	format string,
	color string,
) error {
	if format == "json" {
		return writeJSON(writer, report)
	}
	writeTextStyled(writer, report, shouldUseColor(color, writer))
	return nil
}

type fixProgress struct {
	writer         io.Writer
	style          outputStyle
	mu             sync.Mutex
	agentActive    bool
	agentLineStart bool
}

func newFixProgress(writer io.Writer, color bool) *fixProgress {
	return &fixProgress{
		writer: writer,
		style:  outputStyle{color: color},
	}
}

func (progress *fixProgress) status(message string) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.finishAgentMessageLocked()
	fmt.Fprintf(
		progress.writer,
		"  %s %s\n",
		progress.style.paint("36", "◆"),
		progressLabel(message),
	)
}

func (progress *fixProgress) toolActivity(message string) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.finishAgentMessageLocked()
	fmt.Fprintf(
		progress.writer,
		"  %s %s\n",
		progress.style.paint("36", "↳"),
		message,
	)
}

func (progress *fixProgress) agentMessage(message string) {
	if message == "" {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if !progress.agentActive {
		fmt.Fprintf(
			progress.writer,
			"\n  %s\n",
			progress.style.paint("1;35", "Agent"),
		)
		progress.agentActive = true
		progress.agentLineStart = true
	}
	progress.writeAgentChunk(message)
}

func (progress *fixProgress) finishAgentMessage() {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.finishAgentMessageLocked()
}

func (progress *fixProgress) finishAgentMessageLocked() {
	if !progress.agentActive {
		return
	}
	if !progress.agentLineStart {
		fmt.Fprintln(progress.writer)
	}
	fmt.Fprintln(progress.writer)
	progress.agentActive = false
	progress.agentLineStart = false
}

func (progress *fixProgress) writeAgentChunk(message string) {
	for message != "" {
		if progress.agentLineStart {
			fmt.Fprintf(
				progress.writer,
				"  %s ",
				progress.style.paint("35", "│"),
			)
			progress.agentLineStart = false
		}
		newline := strings.IndexByte(message, '\n')
		if newline < 0 {
			fmt.Fprint(progress.writer, message)
			return
		}
		fmt.Fprintln(progress.writer, message[:newline])
		progress.agentLineStart = true
		message = message[newline+1:]
	}
}

func progressLabel(message string) string {
	if message == "" {
		return ""
	}
	return strings.ToUpper(message[:1]) + message[1:]
}

func writeJSON(writer io.Writer, report runner.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func writeText(writer io.Writer, report runner.Report) {
	writeTextStyled(writer, report, false)
}

func writeTextStyled(writer io.Writer, report runner.Report, color bool) {
	style := outputStyle{color: color}
	for _, finding := range report.Findings {
		writeFinding(writer, style, finding)
	}
	writeSummary(writer, style, report.Findings)
	writeReportTotals(writer, report)
}

func writeFinding(writer io.Writer, style outputStyle, finding runner.Finding) {
	severity := strings.ToUpper(string(finding.Severity))
	fmt.Fprintln(
		writer,
		style.severity(finding.Severity, "✗ "+severity+"  "+finding.RuleID),
	)
	writeHighlightedDescription(writer, style, finding.Severity, finding.Description)

	location := fmt.Sprintf("%s:%d", finding.Path, finding.StartLine)
	if finding.EndLine != finding.StartLine {
		location = fmt.Sprintf("%s-%d", location, finding.EndLine)
	}
	fmt.Fprintf(
		writer,
		"  %s %s %s %s\n",
		style.paint("36", location),
		style.paint("2", "·"),
		finding.Kind,
		finding.Name,
	)
	fmt.Fprintln(writer)
	writeCodeFrame(writer, style, finding)
	fmt.Fprintln(writer)
}

func writeSummary(writer io.Writer, style outputStyle, findings []runner.Finding) {
	if len(findings) == 0 {
		fmt.Fprintln(writer, style.paint("1;32", "✓ No findings"))
		return
	}

	errors, warnings, information := findingCounts(findings)
	fmt.Fprintln(writer, style.paint("1", "Summary"))
	fmt.Fprintf(
		writer,
		"  %s",
		countLabel(len(findings), "finding", "findings"),
	)
	if errors > 0 {
		fmt.Fprintf(
			writer,
			"  %s",
			style.paint("31", countLabel(errors, "error", "errors")),
		)
	}
	if warnings > 0 {
		fmt.Fprintf(
			writer,
			"  %s",
			style.paint("33", countLabel(warnings, "warning", "warnings")),
		)
	}
	if information > 0 {
		fmt.Fprintf(
			writer,
			"  %s",
			style.paint("36", countLabel(information, "info", "info")),
		)
	}
	fmt.Fprintln(writer)
}

func writeReportTotals(writer io.Writer, report runner.Report) {
	fmt.Fprintf(
		writer,
		"  %d files · %d code units · %d evaluations\n",
		report.ScannedFiles,
		report.CodeUnits,
		report.Evaluations,
	)
	if report.Cache != nil {
		fmt.Fprintf(
			writer,
			"  cache · %d hits · %d misses · %d writes\n",
			report.Cache.Hits,
			report.Cache.Misses,
			report.Cache.Writes,
		)
	}
}

type outputStyle struct {
	color bool
}

func (style outputStyle) paint(code string, text string) string {
	if !style.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (style outputStyle) severity(severity config.Severity, text string) string {
	switch severity {
	case config.SeverityError:
		return style.paint("1;31", text)
	case config.SeverityWarning:
		return style.paint("1;33", text)
	default:
		return style.paint("1;36", text)
	}
}

func shouldUseColor(mode string, writer io.Writer) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func findingCounts(findings []runner.Finding) (int, int, int) {
	var errors, warnings, information int
	for _, finding := range findings {
		switch finding.Severity {
		case config.SeverityError:
			errors++
		case config.SeverityWarning:
			warnings++
		default:
			information++
		}
	}
	return errors, warnings, information
}

func countLabel(count int, singular string, plural string) string {
	label := plural
	if count == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", count, label)
}

func writeHighlightedDescription(
	writer io.Writer,
	style outputStyle,
	severity config.Severity,
	description string,
) {
	for _, line := range wrapText(description, 84) {
		fmt.Fprintln(writer, "  "+style.severity(severity, line))
	}
}

func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	lines := make([]string, 0, 1)
	line := ""
	for _, word := range words {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}

func splitFlagsAndPaths(set *flag.FlagSet, args []string) ([]string, []string, error) {
	flagArgs := make([]string, 0, len(args))
	paths := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			paths = append(paths, args[index+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		inline := strings.Contains(name, "=")
		if inline {
			name, _, _ = strings.Cut(name, "=")
		}
		flagArgs = append(flagArgs, arg)
		if inline || name == "h" || name == "help" {
			continue
		}
		defined := set.Lookup(name)
		if defined == nil || isBoolFlag(defined.Value) {
			continue
		}
		if index+1 >= len(args) {
			return nil, nil, fmt.Errorf("flag needs an argument: -%s", name)
		}
		index++
		flagArgs = append(flagArgs, args[index])
	}
	return flagArgs, paths, nil
}

func isBoolFlag(value flag.Value) bool {
	flag, ok := value.(interface{ IsBoolFlag() bool })
	return ok && flag.IsBoolFlag()
}
