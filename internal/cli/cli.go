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

	"jevlint/internal/changed"
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
  --changed             check only git-modified files
  --clear-cache         clear this project's cached evaluations before checking
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default "jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --format text|json    output format (default "text")
  --no-cache            bypass evaluation cache reads and writes
  --refresh-cache       reevaluate and replace current cached results
  --fix                 print findings, then apply a validated fix
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
	commandFix
	commandMCPCheck
	commandHelp
)

func parseCLICommand(name string) cliCommand {
	switch name {
	case "check":
		return commandCheck
	case "fix":
		return commandFix
	case "mcp-check":
		return commandMCPCheck
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
	case commandFix:
		return "fix"
	case commandMCPCheck:
		return "mcp-check"
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
	plainOutput   bool
	dumbTerminal  bool
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
	if dispatch := handleSpecialCommand(ctx, args[0], stdout, stderr, getenv); dispatch.handled {
		return dispatch.exitCode
	}
	options, exitCode, ready := parseRunOptions(parseCLICommand(args[0]), args[1:], stderr)
	if !ready {
		return exitCode
	}
	options.output.hints = readTerminalHints(getenv)
	return executeRun(ctx, options, stdout, stderr, os.UserCacheDir, getenv)
}

type cacheMode int

const (
	cacheReadWrite cacheMode = iota
	cacheBypass
	cacheRefresh
	cacheClear
	cacheClearAndBypass
	cacheClearAndRefresh
)

func (mode cacheMode) shouldBypass() bool {
	return mode == cacheBypass || mode == cacheClearAndBypass
}

func (mode cacheMode) shouldClear() bool {
	return mode == cacheClear ||
		mode == cacheClearAndBypass ||
		mode == cacheClearAndRefresh
}

func (mode cacheMode) shouldRefresh() bool {
	return mode == cacheRefresh || mode == cacheClearAndRefresh
}

type runOptions struct {
	command cliCommand
	paths   []string
	output  outputContext
	check   checkContext
}

type outputContext struct {
	format outputFormat
	color  colorMode
	hints  terminalHints
}

type checkContext struct {
	configPath  string
	concurrency int
	autoFix     bool
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
	ctx context.Context,
	command string,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) commandDispatch {
	switch parseCLICommand(command) {
	case commandMCPCheck:
		return commandDispatch{exitCode: runMCPCheck(ctx, stdout, stderr, getenv), handled: true}
	case commandHelp:
		fmt.Fprint(stderr, usage)
		return commandDispatch{exitCode: exitSuccess, handled: true}
	case commandCheck, commandFix:
		return commandDispatch{exitCode: exitSuccess, handled: false}
	default:
		fmt.Fprintf(stderr, "jevlint: unknown command %q\n\n%s", command, usage)
		return commandDispatch{exitCode: exitUsageError, handled: true}
	}
}

func runMCPCheck(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) int {
	if err := fix.ServeMCP(ctx, os.Stdin, stdout, getenv); err != nil {
		fmt.Fprintf(stderr, "jevlint: mcp-check: %v\n", err)
		return exitUsageError
	}
	return exitSuccess
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
	noCache := flags.Bool("no-cache", false, "bypass evaluation cache")
	refreshCache := flags.Bool("refresh-cache", false, "refresh cached evaluations")
	autoFix := flags.Bool("fix", false, "print findings and apply a validated fix")
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
	parsedFormat, parsedColor, exitCode, valid := validateRunOptions(
		*format,
		*color,
		*concurrency,
		stderr,
	)
	if !valid {
		return runOptions{}, exitCode, false
	}
	bypass := *noCache || *autoFix
	var mode cacheMode
	switch {
	case bypass && *refreshCache:
		if *autoFix && !*noCache {
			fmt.Fprintln(stderr, "jevlint: --fix and --refresh-cache cannot be combined")
		} else {
			fmt.Fprintln(stderr, "jevlint: --no-cache and --refresh-cache cannot be combined")
		}
		return runOptions{}, exitUsageError, false
	case *clearCache && bypass:
		mode = cacheClearAndBypass
	case *clearCache && *refreshCache:
		mode = cacheClearAndRefresh
	case *clearCache:
		mode = cacheClear
	case bypass:
		mode = cacheBypass
	case *refreshCache:
		mode = cacheRefresh
	default:
		mode = cacheReadWrite
	}
	return runOptions{
		command: command,
		paths:   paths,
		output: outputContext{
			format: parsedFormat,
			color:  parsedColor,
		},
		check: checkContext{
			configPath:  *configPath,
			concurrency: *concurrency,
			autoFix:     *autoFix,
			changed:     *changedFiles,
			cache:       mode,
		},
	}, exitSuccess, true
}

const (
	defaultConfigFile        = "jevlint.json"
	defaultCheckConcurrency  = 4
)

func validateRunOptions(
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
	getenv func(string) string,
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
	return runLoaded(ctx, loaded, stdout, stderr, getenv)
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
	if mode.shouldBypass() && !mode.shouldClear() {
		return nil, 0
	}
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
	if mode.shouldBypass() {
		return nil, exitSuccess
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
	getenv func(string) string,
) int {
	options := loaded.options
	if options.command == commandFix || options.check.autoFix {
		newFixProgress(
			stderr,
			shouldUseColor(options.output.color, stderr, options.output.hints),
		).writeProgress("checking for findings")
	}
	report, err := runner.Runner{
		Extractor: loaded.extractor,
		Evaluator: loaded.evaluator,
	}.Evaluate(ctx, loaded.cfg, runner.Options{
		Root:        filepath.Dir(loaded.absoluteConfig),
		Paths:       options.paths,
		Concurrency: options.check.concurrency,
	})
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	return reportCheckResult(ctx, loaded, stdout, stderr, report, getenv)
}

func reportCheckResult(
	ctx context.Context,
	loaded loadedRun,
	stdout io.Writer,
	stderr io.Writer,
	report runner.Report,
	getenv func(string) string,
) int {
	if loaded.options.check.autoFix {
		return completeAutoFix(ctx, loaded, stdout, stderr, report, getenv)
	}
	if loaded.options.command == commandFix {
		return runFix(
			ctx,
			stdout,
			stderr,
			loaded.cfg,
			loaded.absoluteConfig,
			loaded.extractor,
			report,
			loaded.options.check.concurrency,
			loaded.options.output,
			getenv,
		)
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

func completeAutoFix(
	ctx context.Context,
	loaded loadedRun,
	stdout io.Writer,
	stderr io.Writer,
	report runner.Report,
	getenv func(string) string,
) int {
	if loaded.options.output.format == formatText || !report.HasFailures() {
		if exitCode := writeReportOrFail(
			stdout,
			stderr,
			report,
			loaded.options.output,
		); exitCode != 0 {
			return exitCode
		}
	}
	if !report.HasFailures() {
		return exitSuccess
	}
	return runFix(
		ctx,
		stdout,
		stderr,
		loaded.cfg,
		loaded.absoluteConfig,
		loaded.extractor,
		report,
		loaded.options.check.concurrency,
		loaded.options.output,
		getenv,
	)
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
	output outputContext,
	getenv func(string) string,
) int {
	if cfg.Fix == nil {
		fmt.Fprintln(stderr, "jevlint: fix.command is required for the fix command")
		return exitUsageError
	}
	if !report.HasFailures() {
		if err := writeReport(stdout, report, output); err != nil {
			fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
			return exitUsageError
		}
		return exitSuccess
	}

	progress := newFixProgress(stderr, shouldUseColor(output.color, stderr, output.hints))
	proposal, err := prepareFixProposal(
		ctx,
		cfg,
		configPath,
		extractor,
		report,
		progress.writeProgress,
		progress.agentMessage,
		progress.toolActivity,
	)
	progress.mu.Lock()
	progress.finishAgentMessageLocked()
	progress.mu.Unlock()
	if err != nil {
		return reportFixPreparationError(stderr, err)
	}
	progress.writeProgress("validating proposed changes with Jev")
	overlay := make(map[string][]byte, len(proposal.Changes))
	for _, change := range proposal.Changes {
		overlay[change.Path] = change.After
	}
	validationEvaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{},
		getenv,
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: validate fix: %v\n", err)
		return exitUsageError
	}
	validation, err := (runner.Runner{
		Extractor: extractor,
		Evaluator: validationEvaluator,
	}).Evaluate(ctx, cfg, runner.Options{
		Root:          filepath.Dir(configPath),
		Paths:         findingPaths(report.Findings),
		Concurrency:   concurrency,
		SourceOverlay: overlay,
	})
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: validate fix: %v\n", err)
		return exitUsageError
	}
	return finishFix(
		stdout,
		stderr,
		proposal,
		validation,
		output,
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
		Workspace: fix.Workspace{
			Root:       filepath.Dir(configPath),
			ConfigPath: configPath,
			Paths: fix.PathFilter{
				Context: cfg.Fix.Context,
				Exclude: cfg.Fix.Exclude,
			},
		},
		Agent: fix.AgentSession{
			Command: cfg.Fix.Command,
			Feedback: fix.AgentFeedback{
				Progress: progress,
				Message:  agentMessage,
				Tool:     toolActivity,
			},
		},
		Findings: report.Findings,
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
		return exitHasFindings
	}
	fmt.Fprintf(stderr, "jevlint: generate fix: %v\n", err)
	return exitUsageError
}

func finishFix(
	stdout io.Writer,
	stderr io.Writer,
	proposal fix.Proposal,
	validation runner.Report,
	output outputContext,
	projectRoot string,
) int {
	if validation.HasFailures() {
		if err := fix.RestoreChanges(projectRoot, proposal.Changes); err != nil {
			fmt.Fprintf(stderr, "jevlint: restore rejected fix: %v\n", err)
			return exitUsageError
		}
		return writeRejectedFix(
			stdout,
			stderr,
			proposal,
			validation,
			output,
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
		output,
	); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return exitUsageError
	}
	return exitSuccess
}

func writeRejectedFix(
	stdout io.Writer,
	stderr io.Writer,
	proposal fix.Proposal,
	validation runner.Report,
	output outputContext,
) int {
	if err := writeFixOutput(
		stdout,
		fixOutput{
			ModifiedFiles: proposalPaths(proposal),
			Findings:      validation.Findings,
		},
		output,
	); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return exitUsageError
	}
	fmt.Fprintln(stderr, "jevlint: proposed fix did not resolve every finding")
	return exitHasFindings
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
	outputCtx outputContext,
) error {
	if outputCtx.format == formatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	style := outputStyle{color: shouldUseColor(outputCtx.color, writer, outputCtx.hints)}
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

func (progress *fixProgress) writeProgress(message string) {
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

