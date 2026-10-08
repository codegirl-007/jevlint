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
	"time"

	"github.com/codegirl-007/jevlint/internal/changed"
	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/evaluation"
	"github.com/codegirl-007/jevlint/internal/packs"
	"github.com/codegirl-007/jevlint/internal/parsing"
	"github.com/codegirl-007/jevlint/internal/runner"
)

const usage = `Usage:
  jevlint check [flags] [paths...]
  jevlint eval [flags]
  jevlint init [flags]
  jevlint doctor [flags]
  jevlint plugin init|install|list|update|remove [args]
  jevlint version

Check flags:
  --changed             check only git-modified files
  --clear-cache         clear this project's cached evaluations before checking
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default ".jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --format text|json    output format (default "text")
  --refresh-cache       reevaluate and replace current cached results
  --timed-run           print wall-clock run time plus provider and model

Eval flags:
  --clear-cache         clear this project's cached evaluations before evaluating
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default ".jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --evals path          eval cases (default "jevlint-evals.json" next to --config)
  --format text|json    output format (default "text")
  --packs               also run evals from extended packs
  --refresh-cache       reevaluate and replace current cached results
  --rule id             evaluate only this rule's cases
  --verbose             show per-unit Jev decisions

Plugin:
  jevlint plugin install <github-url>
  jevlint plugin list
  jevlint plugin update [id]
  jevlint plugin remove <id>
`

const evalUsage = `Usage:
  jevlint eval [flags]

Flags:
  --clear-cache         clear this project's cached evaluations before evaluating
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default ".jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --evals path          eval cases (default "jevlint-evals.json" next to --config)
  --format text|json    output format (default "text")
  --packs               also run evals from extended packs
  --refresh-cache       reevaluate and replace current cached results
  --rule id             evaluate only this rule's cases
  --verbose             show per-unit Jev decisions
`

// pluginUsage returns the help text for `jevlint plugin`.
func pluginUsage() string {
	return `Usage:
  jevlint plugin init <owner/name> [directory]
  jevlint plugin install <github-url>
  jevlint plugin list
  jevlint plugin update [id]
  jevlint plugin remove <id>

Init flags:
  --languages list      comma-separated languages (default "go")
                        supported: ` + strings.Join(supportedLanguages(), ", ") + `
  --dir path            output directory (default: the pack name)
  --force               write into a non-empty directory

Flags:
  --config path         rule configuration (default ".jevlint.json")
`
}

const (
	exitSuccess     = 0
	exitHasFindings = 1
	exitUsageError  = 2
)

// cliCommand names a top level command the tool accepts.
type cliCommand int

const (
	commandUnknown cliCommand = iota
	commandCheck
	commandEval
	commandInit
	commandDoctor
	commandPlugin
	commandHelp
	commandVersion
)

// outputFormat selects text or json output.
type outputFormat int

const (
	formatUnknown outputFormat = iota
	formatText
	formatJSON
)

// colorMode controls when colored output is used.
type colorMode int

const (
	colorUnknown colorMode = iota
	colorAuto
	colorAlways
	colorNever
)

// terminalHints records what the environment says about the terminal.
type terminalHints struct {
	plainOutput  bool
	dumbTerminal bool
}

// cacheMode says how saved results are used on a run.
type cacheMode int

const (
	cacheReadWrite cacheMode = iota
	cacheRefresh
	cacheClear
	cacheClearAndRefresh
)

// runOptions holds the settings for a check run.
type runOptions struct {
	paths  []string
	output outputContext
	check  checkContext
}

// outputContext holds how results are printed.
type outputContext struct {
	format outputFormat
	color  colorMode
	hints  terminalHints
}

// checkContext holds the settings that shape a check.
type checkContext struct {
	configPath  string
	concurrency int
	changed     bool
	cache       cacheMode
	timedRun    bool
}

// loadedRun holds the config, parser, and client for a check.
type loadedRun struct {
	options        runOptions
	absoluteConfig string
	cfg            config.Config
	extractor      *parsing.Extractor
	evaluator      evaluation.Evaluator
}

// commandDispatch reports whether a command was handled early.
type commandDispatch struct {
	exitCode int
	handled  bool
}

// outputStyle paints text when color is enabled.
type outputStyle struct {
	color bool
}

// parseCLICommand turns a command name into a command value.
func parseCLICommand(name string) cliCommand {
	switch name {
	case "check":
		return commandCheck
	case "eval":
		return commandEval
	case "init":
		return commandInit
	case "doctor":
		return commandDoctor
	case "plugin":
		return commandPlugin
	case "-h", "--help":
		return commandHelp
	case "-v", "--version", "version":
		return commandVersion
	default:
		return commandUnknown
	}
}

// String returns the command name.
func (command cliCommand) String() string {
	switch command {
	case commandCheck:
		return "check"
	case commandEval:
		return "eval"
	case commandInit:
		return "init"
	case commandDoctor:
		return "doctor"
	case commandPlugin:
		return "plugin"
	case commandHelp:
		return "help"
	case commandVersion:
		return "version"
	default:
		return ""
	}
}

// lookupOutputFormat turns a format name into a format value, reporting whether
// the name is known.
func lookupOutputFormat(value string) (outputFormat, bool) {
	switch value {
	case "text":
		return formatText, true
	case "json":
		return formatJSON, true
	default:
		return formatUnknown, false
	}
}

// lookupColorMode turns a color name into a color mode, reporting whether the
// name is known.
func lookupColorMode(value string) (colorMode, bool) {
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

// readTerminalHints reads what the environment says about the terminal.
func readTerminalHints(getenv func(string) string) terminalHints {
	return terminalHints{
		plainOutput:  getenv("NO_COLOR") != "",
		dumbTerminal: getenv("TERM") == "dumb",
	}
}

// Run reads the arguments, runs the requested command, and returns an exit code.
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
	command := parseCLICommand(args[0])
	if dispatch := handleSpecialCommand(args[0], stdout, stderr); dispatch.handled {
		return dispatch.exitCode
	}
	if command == commandInit {
		return executeInit(args[1:], stdout, stderr)
	}
	if command == commandDoctor {
		return executeDoctor(ctx, args[1:], stdout, stderr)
	}
	if command == commandPlugin {
		return executePlugin(args[1:], stdout, stderr, os.UserCacheDir)
	}
	if command == commandEval {
		options, exitCode, ready := parseEvalOptions(args[1:], stderr)
		if !ready {
			return exitCode
		}
		options.output.hints = readTerminalHints(getenv)
		return executeEval(ctx, options, stdout, stderr, os.UserCacheDir)
	}
	options, exitCode, ready := parseRunOptions(command, args[1:], stderr)
	if !ready {
		return exitCode
	}
	options.output.hints = readTerminalHints(getenv)
	return executeRun(ctx, options, stdout, stderr, os.UserCacheDir)
}

// shouldClear reports whether the saved results should be removed.
func (mode cacheMode) shouldClear() bool {
	return mode == cacheClear || mode == cacheClearAndRefresh
}

// shouldRefresh reports whether saved results should be replaced.
func (mode cacheMode) shouldRefresh() bool {
	return mode == cacheRefresh || mode == cacheClearAndRefresh
}

// handleSpecialCommand prints help or an error for commands that do not run.
func handleSpecialCommand(
	command string,
	stdout io.Writer,
	stderr io.Writer,
) commandDispatch {
	switch parseCLICommand(command) {
	case commandHelp:
		fmt.Fprint(stderr, usage)
		return commandDispatch{exitCode: exitSuccess, handled: true}
	case commandVersion:
		fmt.Fprintf(stdout, "jevlint %s\n", Version())
		return commandDispatch{exitCode: exitSuccess, handled: true}
	case commandCheck, commandEval, commandInit, commandDoctor, commandPlugin:
		return commandDispatch{exitCode: exitSuccess, handled: false}
	default:
		fmt.Fprintf(stderr, "jevlint: unknown command %q\n\n%s", command, usage)
		return commandDispatch{exitCode: exitUsageError, handled: true}
	}
}

// newRunFlagSet builds the flag set shared by the check options.
func newRunFlagSet(command cliCommand, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(command.String(), flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
	}
	return flags
}

// parseRunOptions reads the check flags and the paths.
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
	timedRun := flags.Bool("timed-run", false, "print wall-clock run time for provider comparison")
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
			timedRun:    *timedRun,
		},
	}, exitSuccess, true
}

const defaultCheckConcurrency = 4

// parseOutputOptions checks the format, color, and concurrency values.
func parseOutputOptions(
	format string,
	color string,
	concurrency int,
	stderr io.Writer,
) (outputFormat, colorMode, int, bool) {
	parsedFormat, ok := lookupOutputFormat(format)
	if !ok {
		fmt.Fprintf(stderr, "jevlint: --format must be text or json\n")
		return formatUnknown, colorUnknown, exitUsageError, false
	}
	parsedColor, ok := lookupColorMode(color)
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

// executeRun narrows the run to changed files and runs a check.
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

// applyChangedFilter narrows the run to the files git reports as changed.
func applyChangedFilter(
	options runOptions,
	stderr io.Writer,
) (runOptions, int, bool) {
	if !options.check.changed {
		return options, 0, true
	}
	absoluteConfig, err := resolveConfigPath(options.check.configPath)
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

// loadRun loads the config and opens the client and the cache.
func loadRun(
	options runOptions,
	stderr io.Writer,
	userCacheDir func() (string, error),
) (loadedRun, int) {
	absoluteConfig, cfg, extractor, _, exitCode := loadProject(
		options.check.configPath,
		stderr,
		userCacheDir,
	)
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
	evaluator, err := evaluation.NewClientFromEnv(
		evaluation.Options{
			Cache:   resultCache,
			Refresh: options.check.cache.shouldRefresh(),
			Logf:    debugLogger(stderr),
			Warnf:   warnLogger(stderr),
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

// debugLogger returns a request logger when JEVLINT_DEBUG is set. It writes to
// stderr so it never mixes with JSON output on stdout. Credentials are never
// logged.
func debugLogger(stderr io.Writer) func(string, ...any) {
	if strings.TrimSpace(os.Getenv("JEVLINT_DEBUG")) == "" {
		return nil
	}
	return func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
}

// warnLogger writes warnings to stderr.
func warnLogger(stderr io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
}

// loadProject loads the config and builds the parser.
func loadProject(
	configPath string,
	stderr io.Writer,
	userCacheDir func() (string, error),
) (string, config.Config, *parsing.Extractor, []packs.Loaded, int) {
	absoluteConfig, err := resolveConfigPath(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return "", config.Config{}, nil, nil, exitUsageError
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return "", config.Config{}, nil, nil, exitUsageError
	}
	var loadedPacks []packs.Loaded
	if len(cfg.Packs) > 0 {
		loadedPacks, err = packs.Resolve(cfg.Packs, userCacheDir)
		if err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return "", config.Config{}, nil, nil, exitUsageError
		}
		cfg, err = packs.Merge(cfg, loadedPacks)
		if err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return "", config.Config{}, nil, nil, exitUsageError
		}
	}
	if len(cfg.Rules) == 0 {
		fmt.Fprintln(stderr, "jevlint: warning: no rules are configured; nothing will be checked")
	}
	extractor, err := parsing.NewExtractor(cfg.Languages)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: configure languages: %v\n", err)
		return "", config.Config{}, nil, nil, exitUsageError
	}
	return absoluteConfig, cfg, extractor, loadedPacks, exitSuccess
}

// openResultCache opens the saved results and clears them when asked.
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

// handleCacheOpenError decides whether a cache problem stops the run.
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

// runLoaded runs the check and writes the report.
func runLoaded(
	ctx context.Context,
	loaded loadedRun,
	stdout io.Writer,
	stderr io.Writer,
) int {
	var backend evaluation.BackendInfo
	if info, ok := loaded.evaluator.(evaluation.BackendInfo); ok {
		backend = info
	}
	start := time.Now()
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
	attachRunTiming(&report, start, loaded.options.check.timedRun, backend)
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

// writeReportOrFail writes the report and reports a write error.
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

// writeReport prints the findings and the totals in the chosen format.
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

// writeFinding prints one finding with its code frame.
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

// writeSummary prints the counts of the findings.
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

// writeReportTotals prints the file, unit, evaluation, and cache counts.
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
	writeRunTiming(writer, report.Timing)
}

// paint wraps text in a color code when color is enabled.
func (style outputStyle) paint(code string, text string) string {
	if !style.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// severity paints text with the color for a finding level.
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

// shouldUseColor reports whether colored output should be used.
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

// findingCounts counts the findings by level.
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

// countLabel prints a count with the right singular or plural word.
func countLabel(count int, singular string, plural string) string {
	label := plural
	if count == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", count, label)
}

// wrapText breaks text into lines no wider than the limit.
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

// splitFlagsAndPaths separates flags from path arguments.
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

// isPathArg reports whether an argument is a path rather than a flag.
func isPathArg(arg string) bool {
	return arg == "-" || !strings.HasPrefix(arg, "-")
}

// consumeFlagArg returns a flag and its value when the flag takes one.
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

// flagName returns the flag name and whether its value was written inline.
func flagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	inline := strings.Contains(name, "=")
	if inline {
		name, _, _ = strings.Cut(name, "=")
	}
	return name, inline
}
