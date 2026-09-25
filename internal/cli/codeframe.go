package cli

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"

	"jevlint/internal/runner"
)

const tabWidth = 4

func writeCodeFrame(writer io.Writer, style outputStyle, finding runner.Finding) {
	rawLines := strings.Split(finding.Snippet, "\n")
	expandedLines := make([]string, len(rawLines))
	for index, line := range rawLines {
		expandedLines[index] = expandTabs(line, tabWidth)
	}
	displayLines := highlightedLines(
		strings.Join(expandedLines, "\n"),
		finding.Language,
		style.color,
	)
	if len(displayLines) != len(rawLines) {
		displayLines = expandedLines
	}

	lineNumberWidth := len(strconv.FormatUint(uint64(finding.EndLine), 10))
	for index, line := range displayLines {
		lineNumber := finding.StartLine + uint(index)
		fmt.Fprintf(
			writer,
			"  %*d %s %s\n",
			lineNumberWidth,
			lineNumber,
			style.paint("36", "│"),
			line,
		)
		for _, location := range finding.Locations {
			start, length, ok := pointerForLine(
				rawLines[index],
				lineNumber,
				location,
				tabWidth,
			)
			if !ok {
				continue
			}
			fmt.Fprintf(
				writer,
				"  %*s %s %s%s\n",
				lineNumberWidth,
				"",
				style.paint("36", "│"),
				strings.Repeat(" ", start),
				style.severity(
					finding.Severity,
					strings.Repeat("^", length),
				),
			)
		}
	}
}

func highlightedLines(source string, language string, color bool) []string {
	if !color {
		return strings.Split(source, "\n")
	}

	lexer := map[string]string{
		"c":          "c",
		"cpp":        "cpp",
		"csharp":     "csharp",
		"java":       "java",
		"javascript": "javascript",
		"kotlin":     "kotlin",
		"php":        "php",
		"ruby":       "ruby",
		"typescript": "typescript",
		"tsx":        "tsx",
		"python":     "python",
		"go":         "go",
		"rust":       "rust",
	}[language]
	if lexer == "" {
		return strings.Split(source, "\n")
	}

	var highlighted bytes.Buffer
	if err := quick.Highlight(
		&highlighted,
		source,
		lexer,
		"terminal256",
		"github-dark",
	); err != nil {
		return strings.Split(source, "\n")
	}
	output := strings.TrimSuffix(highlighted.String(), "\n")
	return strings.Split(output, "\n")
}

func pointerForLine(
	rawLine string,
	lineNumber uint,
	location runner.Location,
	tabSize int,
) (int, int, bool) {
	if lineNumber < location.StartLine || lineNumber > location.EndLine {
		return 0, 0, false
	}

	startColumn := uint(0)
	if lineNumber == location.StartLine {
		startColumn = location.StartColumn
	}
	endColumn := uint(len(rawLine))
	if lineNumber == location.EndLine {
		endColumn = location.EndColumn
	}
	if startColumn > uint(len(rawLine)) {
		startColumn = uint(len(rawLine))
	}
	if endColumn > uint(len(rawLine)) {
		endColumn = uint(len(rawLine))
	}
	if endColumn <= startColumn {
		endColumn = startColumn + 1
	}

	start := visualColumn(rawLine, int(startColumn), tabSize)
	end := visualColumn(rawLine, int(endColumn), tabSize)
	if end <= start {
		end = start + 1
	}
	return start, end - start, true
}

func expandTabs(line string, tabSize int) string {
	var builder strings.Builder
	column := 0
	for _, value := range line {
		if value != '\t' {
			builder.WriteRune(value)
			column++
			continue
		}
		spaces := tabSize - column%tabSize
		builder.WriteString(strings.Repeat(" ", spaces))
		column += spaces
	}
	return builder.String()
}

func visualColumn(line string, byteColumn int, tabSize int) int {
	if byteColumn > len(line) {
		byteColumn = len(line)
	}
	column := 0
	for index := 0; index < byteColumn; index++ {
		if line[index] != '\t' {
			column++
			continue
		}
		column += tabSize - column%tabSize
	}
	return column
}
