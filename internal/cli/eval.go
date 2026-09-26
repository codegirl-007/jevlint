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

	"jevlint/internal/config"
	"jevlint/internal/evals"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
)

type evalOptions struct {
	output outputContext
	eval   evalContext
}

type evalContext struct {
	configPath  string
	evalsPath   string
	ruleID      string
	concurrency int
	cache       cacheMode
}

func parseEvalOptions(args []string, stderr io.Writer) (evalOptions, int, bool) {
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, evalUsage)
	}
	clearCache := flags.Bool("clear-cache", false, "clear cached evaluations")
	color := flags.String("color", "auto", "color output")
	configPath := flags.String("config", defaultConfigFile, "rule configuration")
	concurrency := flags.Int("concurrency", defaultCheckConcurrency, "maximum concurrent Jev requests")
	evalsPath := flags.String("evals", "", "eval cases")
	format := flags.String("format", "text", "output format")
	refreshCache := flags.Bool("refresh-cache", false, "refresh cached evaluations")
	ruleID := flags.String("rule", "", "evaluate only this rule")
	flagArgs, leftover, err := splitFlagsAndPaths(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return evalOptions{}, exitUsageError, false
	}
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return evalOptions{}, exitSuccess, false
		}
		return evalOptions{}, exitUsageError, false
	}
	if len(leftover) > 0 {
		fmt.Fprintf(stderr, "jevlint: eval does not take path arguments\n")
		return evalOptions{}, exitUsageError, false
	}
	parsedFormat, parsedColor, exitCode, valid := parseOutputOptions(
		*format,
		*color,
		*concurrency,
		stderr,
	)
	if !valid {
		return evalOptions{}, exitCode, false
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
	return evalOptions{
		output: outputContext{
			format: parsedFormat,
			color:  parsedColor,
		},
		eval: evalContext{
			configPath:  *configPath,
			evalsPath:   *evalsPath,
			ruleID:      *ruleID,
			concurrency: *concurrency,
			cache:       mode,
		},
	}, exitSuccess, true
}

func executeEval(
	ctx context.Context,
	options evalOptions,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	absoluteConfig, cfg, extractor, exitCode := loadProject(options.eval.configPath, stderr)
	if exitCode != 0 {
		return exitCode
	}
	document, exitCode := loadEvalDocument(
		absoluteConfig,
		options.eval.evalsPath,
		cfg,
		extractor,
		stderr,
	)
	if exitCode != 0 {
		return exitCode
	}
	document, err := document.FilterRule(options.eval.ruleID)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	resultCache, exitCode := openResultCache(
		filepath.Dir(absoluteConfig),
		options.eval.cache,
		stderr,
		userCacheDir,
	)
	if exitCode != 0 {
		return exitCode
	}
	evaluator, err := evaluation.NewTypeSafeFromEnvWithOptions(
		evaluation.TypeSafeOptions{
			Cache:   resultCache,
			Refresh: options.eval.cache.shouldRefresh(),
		},
		os.Getenv,
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	report, err := evals.Run(
		ctx,
		document,
		cfg,
		extractor,
		evaluator,
		evals.Options{
			Root:        filepath.Dir(absoluteConfig),
			Concurrency: options.eval.concurrency,
		},
	)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	if err := writeEvalReport(stdout, report, options.output); err != nil {
		fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		return exitUsageError
	}
	if report.Mismatched > 0 {
		return exitHasFindings
	}
	return exitSuccess
}

func loadEvalDocument(
	absoluteConfig string,
	evalsPath string,
	cfg config.Config,
	extractor *parsing.Extractor,
	stderr io.Writer,
) (evals.Document, int) {
	path := evalsPath
	if path == "" {
		path = filepath.Join(filepath.Dir(absoluteConfig), evals.DefaultFile)
	}
	document, err := evals.Load(path, cfg, extractor)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return evals.Document{}, exitUsageError
	}
	return document, exitSuccess
}

func writeEvalReport(
	writer io.Writer,
	report evals.Report,
	output outputContext,
) error {
	if output.format == formatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	style := outputStyle{color: shouldUseColor(output.color, writer, output.hints)}
	writeEvalText(writer, style, report)
	return nil
}

func writeEvalText(writer io.Writer, style outputStyle, report evals.Report) {
	currentRule := ""
	for _, result := range report.Cases {
		if result.Rule != currentRule {
			if currentRule != "" {
				fmt.Fprintln(writer)
			}
			fmt.Fprintln(writer, style.paint("1", result.Rule))
			currentRule = result.Rule
		}
		writeEvalCase(writer, style, result)
	}
	if len(report.Cases) > 0 {
		fmt.Fprintln(writer)
	}
	fmt.Fprintf(
		writer,
		"%d/%d eval cases matched expectations\n",
		report.Matched,
		report.Total,
	)
}

func writeEvalCase(writer io.Writer, style outputStyle, result evals.Result) {
	label := result.File
	if result.Name != "" {
		label = result.Name + "  " + result.File
	}
	if result.Matched {
		fmt.Fprintf(
			writer,
			"  %s  expected %s\n",
			style.paint("32", "✓ "+label),
			result.Expected,
		)
		return
	}
	fmt.Fprintf(
		writer,
		"  %s  expected %s  actual %s\n",
		style.paint("31", "✗ "+label),
		result.Expected,
		result.Actual,
	)
}
