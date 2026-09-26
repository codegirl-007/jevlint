package cli

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"

	"jevlint/internal/parsing"
	"jevlint/internal/runner"
)

const frameTabWidth = 4

func writeCodeFrame(writer io.Writer, style outputStyle, finding runner.Finding) {
	lineNumberWidth := len(strconv.FormatUint(uint64(finding.EndLine), 10))
	for index, raw := range strings.Split(finding.Snippet, "\n") {
		expanded := expandTabs(raw, frameTabWidth)
		display := expanded
		if highlighted := highlightedLines(expanded, finding.Language, style.color); len(highlighted) == 1 {
			display = highlighted[0]
		}
		lineNumber := finding.StartLine + uint(index)
		writeCodeFrameLine(writer, style, lineNumberWidth, lineNumber, display)
		for _, location := range finding.Locations {
			writeCodeFramePointer(
				writer,
				style,
				finding,
				raw,
				lineNumber,
				lineNumberWidth,
				location,
			)
		}
	}
}

func writeCodeFrameLine(
	writer io.Writer,
	style outputStyle,
	lineNumberWidth int,
	lineNumber uint,
	line string,
) {
	fmt.Fprintf(
		writer,
		"  %*d %s %s\n",
		lineNumberWidth,
		lineNumber,
		style.paint("36", "│"),
		line,
	)
}

func writeCodeFramePointer(
	writer io.Writer,
	style outputStyle,
	finding runner.Finding,
	rawLine string,
	lineNumber uint,
	lineNumberWidth int,
	location runner.Location,
) {
	start, length, ok := pointerForLine(
		rawLine,
		lineNumber,
		displayLocation(finding, location),
		frameTabWidth,
	)
	if !ok {
		return
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

func displayLocation(
	finding runner.Finding,
	location runner.Location,
) runner.Location {
	if location.StartLine != finding.StartLine || finding.StartColumn == 0 {
		return location
	}
	offset := finding.StartColumn
	if location.StartColumn >= offset {
		location.StartColumn -= offset
	}
	if location.EndLine == finding.StartLine && location.EndColumn >= offset {
		location.EndColumn -= offset
	}
	return location
}

func highlightedLines(source string, language string, color bool) []string {
	if !color {
		return strings.Split(source, "\n")
	}

	sourceLanguage, ok := parsing.ParseSourceLanguage(language)
	if !ok {
		return strings.Split(source, "\n")
	}
	lexer := sourceLanguage.String()

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
