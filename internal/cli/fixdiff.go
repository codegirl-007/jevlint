package cli

import (
	"path/filepath"
	"strings"

	"jevlint/internal/parsing"
)

const (
	diffMetaColor = "36"
	diffAddColor  = "32"
	diffDelColor  = "31"
)

func highlightedFixDiff(diff string, color bool) string {
	if !color {
		return diff
	}
	style := outputStyle{color: true}
	lines := strings.Split(diff, "\n")
	var output strings.Builder
	language := ""
	for index, line := range lines {
		if index > 0 {
			output.WriteByte('\n')
		}
		language = writeColoredDiffLine(&output, style, line, language)
	}
	return output.String()
}

func writeColoredDiffLine(
	output *strings.Builder,
	style outputStyle,
	line string,
	language string,
) string {
	switch {
	case strings.HasPrefix(line, "+++ b/"):
		language = languageForPath(strings.TrimPrefix(line, "+++ b/"))
		output.WriteString(style.paint(diffMetaColor, line))
	case strings.HasPrefix(line, "--- "),
		strings.HasPrefix(line, "@@"):
		output.WriteString(style.paint(diffMetaColor, line))
	case strings.HasPrefix(line, "+"):
		writeColoredDiffContent(output, style, diffAddColor, "+", line[1:], language)
	case strings.HasPrefix(line, "-"):
		writeColoredDiffContent(output, style, diffDelColor, "-", line[1:], language)
	case strings.HasPrefix(line, " "):
		writeColoredDiffContent(output, style, "", " ", line[1:], language)
	default:
		output.WriteString(line)
	}
	return language
}

func writeColoredDiffContent(
	output *strings.Builder,
	style outputStyle,
	color string,
	marker string,
	content string,
	language string,
) {
	if highlighted := highlightedLines(content, language, true); len(highlighted) == 1 {
		content = highlighted[0]
	}
	if color == "" {
		output.WriteString(marker)
	} else {
		output.WriteString(style.paint(color, marker))
	}
	output.WriteString(content)
}

func languageForPath(path string) string {
	language, ok := languageForExtension[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return ""
	}
	return language.String()
}

var languageForExtension = map[string]parsing.SourceLanguage{
	".c":       parsing.SourceLanguageC,
	".cc":      parsing.SourceLanguageCPP,
	".cpp":     parsing.SourceLanguageCPP,
	".cs":      parsing.SourceLanguageCSharp,
	".cts":     parsing.SourceLanguageTypeScript,
	".cxx":     parsing.SourceLanguageCPP,
	".go":      parsing.SourceLanguageGo,
	".h":       parsing.SourceLanguageCPP,
	".hpp":     parsing.SourceLanguageCPP,
	".hxx":     parsing.SourceLanguageCPP,
	".java":    parsing.SourceLanguageJava,
	".js":      parsing.SourceLanguageJavaScript,
	".jsx":     parsing.SourceLanguageJavaScript,
	".kt":      parsing.SourceLanguageKotlin,
	".kts":     parsing.SourceLanguageKotlin,
	".mjs":     parsing.SourceLanguageJavaScript,
	".mts":     parsing.SourceLanguageTypeScript,
	".php":     parsing.SourceLanguagePHP,
	".phtml":   parsing.SourceLanguagePHP,
	".py":      parsing.SourceLanguagePython,
	".rake":    parsing.SourceLanguageRuby,
	".rb":      parsing.SourceLanguageRuby,
	".rs":      parsing.SourceLanguageRust,
	".tsx":     parsing.SourceLanguageTSX,
	".ts":      parsing.SourceLanguageTypeScript,
	".gemspec": parsing.SourceLanguageRuby,
}
