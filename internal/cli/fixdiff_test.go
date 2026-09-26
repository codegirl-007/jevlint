package cli

import (
	"strings"
	"testing"
)

func TestHighlightedFixDiffPreservesPlainOutput(t *testing.T) {
	t.Parallel()

	diff := "--- a/sample.go\n+++ b/sample.go\n@@ -1 +1 @@\n-func Old() {}\n+func New() {}\n"
	if got := highlightedFixDiff(diff, false); got != diff {
		t.Fatalf("highlightedFixDiff() = %q, want unchanged diff", got)
	}
}

func TestHighlightedFixDiffColorsMarkersAndCode(t *testing.T) {
	t.Parallel()

	diff := "--- a/sample.go\n+++ b/sample.go\n@@ -1 +1 @@\n-func Old() {}\n+func New() {}\n"
	output := highlightedFixDiff(diff, true)
	for _, expected := range []string{
		"\x1b[36m--- a/sample.go\x1b[0m",
		"\x1b[31m-\x1b[0m",
		"\x1b[32m+\x1b[0m",
		"\x1b[",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("highlightedFixDiff() = %q, want %q", output, expected)
		}
	}
}

func TestLanguageForPath(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"main.go":       "go",
		"component.tsx": "tsx",
		"header.hpp":    "cpp",
		"unknown.txt":   "",
	}
	for path, want := range tests {
		if got := languageForPath(path); got != want {
			t.Errorf("languageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
