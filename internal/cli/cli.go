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

	"jevlint/internal/config"
	"jevlint/internal/evaluation"
	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const usage = `Usage:
  jevlint check [flags] [paths...]

Flags:
  --color mode          color output: auto, always, or never (default "auto")
  --config path         rule configuration (default "jevlint.json")
  --concurrency number  maximum concurrent Jev requests (default 4)
  --format text|json    output format (default "text")
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if args[0] != "check" {
		fmt.Fprintf(stderr, "jevlint: unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	color := flags.String("color", "auto", "color output")
	configPath := flags.String("config", "jevlint.json", "rule configuration")
	concurrency := flags.Int("concurrency", 4, "maximum concurrent Jev requests")
	format := flags.String("format", "text", "output format")
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
	}
	if err := flags.Parse(args[1:]); err != nil {
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

	evaluator, err := evaluation.NewTypeSafeFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}

	checker := runner.Runner{
		Extractor: parsing.NewExtractor(),
		Evaluator: evaluator,
	}
	report, err := checker.Check(ctx, cfg, runner.Options{
		Root:        filepath.Dir(absoluteConfig),
		Paths:       flags.Args(),
		Concurrency: *concurrency,
	})
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return 2
	}

	switch *format {
	case "json":
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "jevlint: write JSON output: %v\n", err)
			return 2
		}
	case "text":
		writeTextStyled(stdout, report, shouldUseColor(*color, stdout))
	}

	if report.HasFailures() {
		return 1
	}
	return 0
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
