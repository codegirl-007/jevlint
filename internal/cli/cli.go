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

	"jevlint/internal/changed"
	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const usage = `Usage:
  jevlint check [flags] [paths...]

Flags:
  --changed             check only git-modified files
  --clear-cache         clear this project's cached evaluations before checking
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default "jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --format text|json    output format (default "text")
  --refresh-cache       reevaluate and replace current cached results
`

const (
	exitSuccess     = 0
	exitHasFindings = 1
	exitUsageError  = 2
)

type cliCommand int

const (
	commandUnknown cliCommand = iota
	commandCheck
	commandHelp
)

func parseCLICommand(name string) cliCommand {
	switch name {
	case "check":
		return commandCheck
	case "-h", "--help":
		return commandHelp
	default:
		return commandUnknown
	}
}

func (command cliCommand) String() string {
	switch command {
	case commandCheck:
		return "check"
	case commandHelp:
		return "help"
	default:
		return ""
	}
}

type outputFormat int

const (
	formatUnknown outputFormat = iota
	formatText
	formatJSON
)

func parseOutputFormat(value string) (outputFormat, bool) {
	switch value {
	case "text":
		return formatText, true
	case "json":
		return formatJSON, true
	default:
		return formatUnknown, false
	}
}

type colorMode int

const (
	colorUnknown colorMode = iota
	colorAuto
	colorAlways
	colorNever
)

func parseColorMode(value string) (colorMode, bool) {
	switch value {
	case "auto":
		return colorAuto, true
	case "always":
		return colorAlways, true
	case "never":
		return colorNever, true
	default:
		return colorUnknown, false
	}
}

type terminalHints struct {
	plainOutput  bool
	dumbTerminal bool
}

func readTerminalHints(getenv func(string) string) terminalHints {
	return terminalHints{
		plainOutput:  getenv("NO_COLOR") != "",
		dumbTerminal: getenv("TERM") == "dumb",
	}
}

func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsageError
	}
	if dispatch := handleSpecialCommand(args[0], stderr); dispatch.handled {
		return dispatch.exitCode
	}
	options, exitCode, ready := parseRunOptions(parseCLICommand(args[0]), args[1:], stderr)
	if !ready {
		return exitCode
	}
	options.output.hints = readTerminalHints(getenv)
	return executeRun(ctx, options, stdout, stderr, os.UserCacheDir)
}

type cacheMode int

const (
	cacheReadWrite cacheMode = iota
	cacheRefresh
	cacheClear
	cacheClearAndRefresh
)

func (mode cacheMode) shouldClear() bool {
	return mode == cacheClear || mode == cacheClearAndRefresh
}

func (mode cacheMode) shouldRefresh() bool {
	return mode == cacheRefresh || mode == cacheClearAndRefresh
}

type runOptions struct {
	paths  []string
	output outputContext
	check  checkContext
}

type outputContext struct {
	format outputFormat
	color  colorMode
	hints  terminalHints
}

type checkContext struct {
	configPath  string
	concurrency int
	changed     bool
	cache       cacheMode
}

type loadedRun struct {
	options        runOptions
	absoluteConfig string
	cfg            config.Config
	extractor      *parsing.Extractor
	evaluator      evaluation.Evaluator
}

type commandDispatch struct {
	exitCode int
	handled  bool
}

func handleSpecialCommand(
	command string,
	stderr io.Writer,
) commandDispatch {
	switch parseCLICommand(command) {
	case commandHelp:
		fmt.Fprint(stderr, usage)
		return commandDispatch{exitCode: exitSuccess, handled: true}
	case commandCheck:
		return commandDispatch{exitCode: exitSuccess, handled: false}
	default:
		fmt.Fprintf(stderr, "jevlint: unknown command %q\n\n%s", command, usage)
		return commandDispatch{exitCode: exitUsageError, handled: true}
	}
}

func newRunFlagSet(command cliCommand, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(command.String(), flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
	}
	return flags
}

func parseRunOptions(
	command cliCommand,
	args []string,
	stderr io.Writer,
) (runOptions, int, bool) {
	flags := newRunFlagSet(command, stderr)
	changedFiles := flags.Bool("changed", false, "check only git-modified files")
	clearCache := flags.Bool("clear-cache", false, "clear cached evaluations")
	color := flags.String("color", "auto", "color output")
	configPath := flags.String("config", defaultConfigFile, "rule configuration")
	concurrency := flags.Int("concurrency", defaultCheckConcurrency, "maximum concurrent Jev requests")
	format := flags.String("format", "text", "output format")
	refreshCache := flags.Bool("refresh-cache", false, "refresh cached evaluations")
	flagArgs, paths, err := splitFlagsAndPaths(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return runOptions{}, exitUsageError, false
	}
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return runOptions{}, exitSuccess, false
		}
		return runOptions{}, exitUsageError, false
	}
	parsedFormat, parsedColor, exitCode, valid := parseOutputOptions(
		*format,
		*color,
		*concurrency,
		stderr,
	)
	if !valid {
		return runOptions{}, exitCode, false
	}
	var mode cacheMode
	switch {
	case *clearCache && *refreshCache:
		mode = cacheClearAndRefresh
	case *clearCache:
		mode = cacheClear
	case *refreshCache:
		mode = cacheRefresh
	default:
		mode = cacheReadWrite
	}
	return runOptions{
		paths: paths,
		output: outputContext{
			format: parsedFormat,
			color:  parsedColor,
		},
		check: checkContext{
			configPath:  *configPath,
			concurrency: *concurrency,
			changed:     *changedFiles,
			cache:       mode,
		},
	}, exitSuccess, true
}

const (
	defaultConfigFile       = "jevlint.json"
	defaultCheckConcurrency = 4
)

func parseOutputOptions(
	format string,
	color string,
	concurrency int,
	stderr io.Writer,
) (outputFormat, colorMode, int, bool) {
	parsedFormat, ok := parseOutputFormat(format)
	if !ok {
		fmt.Fprintf(stderr, "jevlint: --format must be text or json\n")
		return formatUnknown, colorUnknown, exitUsageError, false
	}
	parsedColor, ok := parseColorMode(color)
	if !ok {
		fmt.Fprintln(stderr, "jevlint: --color must be auto, always, or never")
		return formatUnknown, colorUnknown, exitUsageError, false
	}
	if concurrency < 1 {
		fmt.Fprintln(stderr, "jevlint: --concurrency must be at least 1")
		return formatUnknown, colorUnknown, exitUsageError, false
	}
	return parsedFormat, parsedColor, exitSuccess, true
}

func executeRun(
	ctx context.Context,
	options runOptions,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	options, exitCode, ready := applyChangedFilter(options, stderr)
	if !ready {
		return exitCode
	}
	if options.check.changed && len(options.paths) == 0 {
		return writeReportOrFail(
			stdout,
			stderr,
			runner.Report{},
			options.output,
		)
	}
	loaded, exitCode := loadRun(options, stderr, userCacheDir)
	if exitCode != 0 {
		return exitCode
	}
	return runLoaded(ctx, loaded, stdout, stderr)
}

func applyChangedFilter(
	options runOptions,
	stderr io.Writer,
) (runOptions, int, bool) {
	if !options.check.changed {
		return options, 0, true
	}
	absoluteConfig, err := filepath.Abs(options.check.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return runOptions{}, 2, false
	}
	root := filepath.Dir(absoluteConfig)
	files, err := changed.Files(root)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return runOptions{}, 2, false
	}
	requested, err := changed.Relativize(root, options.paths)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return runOptions{}, 2, false
	}
	options.paths = changed.Intersect(files, requested)
	return options, 0, true
}

func loadRun(
	options runOptions,
	stderr io.Writer,
	userCacheDir func() (string, error),
) (loadedRun, int) {
	absoluteConfig, cfg, extractor, exitCode := loadProject(options.check.configPath, stderr)
	if exitCode != 0 {
		return loadedRun{}, exitCode
	}
	resultCache, exitCode := openResultCache(
		filepath.Dir(absoluteConfig),
		options.check.cache,
		stderr,
		userCacheDir,
	)
	if exitCode != 0 {
		return loadedRun{}, exitCode
	}
	evaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{
			Cache:   resultCache,
			Refresh: options.check.cache.shouldRefresh(),
		},
		os.Getenv,
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return loadedRun{}, exitUsageError
	}
	return loadedRun{
		options:        options,
		absoluteConfig: absoluteConfig,
		cfg:            cfg,
		extractor:      extractor,
		evaluator:      evaluator,
	}, exitSuccess
}

func loadProject(
	configPath string,
	stderr io.Writer,
) (string, config.Config, *parsing.Extractor, int) {
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return "", config.Config{}, nil, exitUsageError
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return "", config.Config{}, nil, exitUsageError
	}
	extractor, err := parsing.NewExtractor(cfg.Languages)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: configure languages: %v\n", err)
		return "", config.Config{}, nil, exitUsageError
	}
	return absoluteConfig, cfg, extractor, exitSuccess
}

func openResultCache(
	projectRoot string,
	mode cacheMode,
	stderr io.Writer,
	userCacheDir func() (string, error),
) (evaluation.ResultCache, int) {
	cache, cacheErr := evaluation.NewFileCache(projectRoot, userCacheDir)
	if cacheErr != nil {
		return handleCacheOpenError(cacheErr, mode.shouldClear(), stderr)
	}
	if mode.shouldClear() {
		if err := cache.Clear(); err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return nil, exitUsageError
		}
	}
	return cache, exitSuccess
}

func handleCacheOpenError(
	cacheErr error,
	clearCache bool,
	stderr io.Writer,
) (evaluation.ResultCache, int) {
	if clearCache {
		fmt.Fprintf(stderr, "jevlint: %v\n", cacheErr)
		return nil, exitUsageError
	}
	fmt.Fprintf(stderr, "jevlint: cache disabled: %v\n", cacheErr)
	return nil, exitSuccess
}

func runLoaded(
	ctx context.Context,
	loaded loadedRun,
	stdout io.Writer,
	stderr io.Writer,
) int {
	report, err := runner.Runner{
		Extractor: loaded.extractor,
		Evaluator: loaded.evaluator,
	}.Evaluate(ctx, loaded.cfg, runner.Options{
		Root:        filepath.Dir(loaded.absoluteConfig),
		Paths:       loaded.options.paths,
		Concurrency: loaded.options.check.concurrency,
	})
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	if exitCode := writeReportOrFail(
		stdout,
		stderr,
		report,
		loaded.options.output,
	); exitCode != 0 {
		return exitCode
	}
	if report.HasFailures() {
		return exitHasFindings
	}
	return exitSuccess
}

func writeReportOrFail(
	stdout io.Writer,
	stderr io.Writer,
	report runner.Report,
	output outputContext,
) int {
	if err := writeReport(stdout, report, output); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return exitUsageError
	}
	return exitSuccess
}

func writeReport(
	writer io.Writer,
	report runner.Report,
	output outputContext,
) error {
	if output.format == formatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	style := outputStyle{color: shouldUseColor(output.color, writer, output.hints)}
	for _, finding := range report.Findings {
		writeFinding(writer, style, finding)
	}
	writeSummary(writer, style, report.Findings)
	writeReportTotals(writer, report)
	return nil
}

func writeFinding(writer io.Writer, style outputStyle, finding runner.Finding) {
	severity := strings.ToUpper(finding.Severity.String())
	fmt.Fprintln(
		writer,
		style.severity(finding.Severity, "✗ "+severity+"  "+finding.RuleID),
	)
	for _, line := range wrapText(finding.Description, 84) {
		fmt.Fprintln(writer, "  "+style.severity(finding.Severity, line))
	}

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

func shouldUseColor(mode colorMode, writer io.Writer, hints terminalHints) bool {
	switch mode {
	case colorAlways:
		return true
	case colorNever:
		return false
	}
	if hints.plainOutput || hints.dumbTerminal {
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
		if isPathArg(arg) {
			paths = append(paths, arg)
			continue
		}
		consumed, next, err := consumeFlagArg(set, args, index)
		if err != nil {
			return nil, nil, err
		}
		flagArgs = append(flagArgs, consumed...)
		index = next
	}
	return flagArgs, paths, nil
}

func isPathArg(arg string) bool {
	return arg == "-" || !strings.HasPrefix(arg, "-")
}

func consumeFlagArg(
	set *flag.FlagSet,
	args []string,
	index int,
) ([]string, int, error) {
	arg := args[index]
	name, inline := flagName(arg)
	if inline || name == "h" || name == "help" {
		return []string{arg}, index, nil
	}
	defined := set.Lookup(name)
	if defined == nil {
		return []string{arg}, index, nil
	}
	if boolFlag, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
		return []string{arg}, index, nil
	}
	if index+1 >= len(args) {
		return nil, index, fmt.Errorf("flag needs an argument: -%s", name)
	}
	return []string{arg, args[index+1]}, index + 1, nil
}

func flagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	inline := strings.Contains(name, "=")
	if inline {
		name, _, _ = strings.Cut(name, "=")
	}
	return name, inline
}
