package cli

import (
	"path/filepath"
	"strings"
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
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			language = languageForPath(strings.TrimPrefix(line, "+++ b/"))
			output.WriteString(style.paint("36", line))
		case strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "@@"):
			output.WriteString(style.paint("36", line))
		case strings.HasPrefix(line, "+"):
			output.WriteString(style.paint("32", "+"))
			output.WriteString(highlightDiffCode(line[1:], language))
		case strings.HasPrefix(line, "-"):
			output.WriteString(style.paint("31", "-"))
			output.WriteString(highlightDiffCode(line[1:], language))
		case strings.HasPrefix(line, " "):
			output.WriteByte(' ')
			output.WriteString(highlightDiffCode(line[1:], language))
		default:
			output.WriteString(line)
		}
	}
	return output.String()
}

func highlightDiffCode(source string, language string) string {
	lines := highlightedLines(source, language, true)
	if len(lines) != 1 {
		return source
	}
	return lines[0]
}

func languageForPath(path string) string {
	return map[string]string{
		".c":       "c",
		".cc":      "cpp",
		".cpp":     "cpp",
		".cs":      "csharp",
		".cts":     "typescript",
		".cxx":     "cpp",
		".go":      "go",
		".h":       "cpp",
		".hpp":     "cpp",
		".hxx":     "cpp",
		".java":    "java",
		".js":      "javascript",
		".jsx":     "javascript",
		".kt":      "kotlin",
		".kts":     "kotlin",
		".mjs":     "javascript",
		".mts":     "typescript",
		".php":     "php",
		".phtml":   "php",
		".py":      "python",
		".rake":    "ruby",
		".rb":      "ruby",
		".rs":      "rust",
		".tsx":     "tsx",
		".ts":      "typescript",
		".gemspec": "ruby",
	}[strings.ToLower(filepath.Ext(path))]
}
