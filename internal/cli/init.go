package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/parsing"
)

// initUsage returns the help text for `jevlint init`.
func initUsage() string {
	return `Usage:
  jevlint init [flags]

Writes a starter .jevlint.json with the languages detected in the project.

Flags:
  --config path      rule configuration to create (default ".jevlint.json")
  --languages list   comma-separated languages (default: detected)
                     supported: ` + strings.Join(supportedLanguages(), ", ") + `
  --force            overwrite files that already exist
  --json             print machine-readable output
`
}

// supportedLanguages returns the preset language names in a stable order.
func supportedLanguages() []string {
	extensions := parsing.PresetExtensions()
	languages := make([]string, 0, len(extensions))
	for language := range extensions {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// defaultInitLanguage is used when the project has no detectable source files.
const defaultInitLanguage = "go"

// initReport is the machine-readable result of `jevlint init --json`.
type initReport struct {
	Config    string   `json:"config"`
	Languages []string `json:"languages"`
}

// executeInit writes a starter config for a project.
func executeInit(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, initUsage()) }
	configPath := flags.String("config", defaultConfigFile, "rule configuration to create")
	languages := flags.String("languages", "", "comma-separated languages")
	force := flags.Bool("force", false, "overwrite existing files")
	jsonOutput := flags.Bool("json", false, "print machine-readable output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsageError
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "jevlint: init does not take arguments")
		return exitUsageError
	}

	existing, err := existingConfigPaths(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return exitUsageError
	}
	if len(existing) > 0 && !*force {
		fmt.Fprintf(
			stderr,
			"jevlint: %s already exists; pass --force to overwrite\n",
			existing[0],
		)
		return exitUsageError
	}
	absolute, err := configPathForInit(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return exitUsageError
	}
	root := filepath.Dir(absolute)

	selected := []string(nil)
	if strings.TrimSpace(*languages) != "" {
		parsed, err := splitLanguages(*languages)
		if err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return exitUsageError
		}
		selected = parsed
	}
	if len(selected) == 0 {
		selected = detectLanguages(root)
	}
	defaulted := false
	if len(selected) == 0 {
		selected = []string{defaultInitLanguage}
		defaulted = true
	}
	for _, language := range selected {
		if _, err := parsing.ParseSourceLanguage(language); err != nil {
			fmt.Fprintf(stderr, "jevlint: unknown language %q\n", language)
			return exitUsageError
		}
	}

	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		fmt.Fprintf(stderr, "jevlint: create %s: %v\n", filepath.Dir(absolute), err)
		return exitUsageError
	}
	cfg := config.Config{Languages: make(map[string]config.Language, len(selected))}
	for _, language := range selected {
		cfg.Languages[language] = config.Language{}
	}
	if err := config.Write(absolute, cfg); err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}

	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(initReport{
			Config:    absolute,
			Languages: selected,
		}); err != nil {
			fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
			return exitUsageError
		}
		return exitSuccess
	}

	if defaulted {
		fmt.Fprintf(
			stdout,
			"no source files detected; defaulting to %s (edit %s to change)\n",
			defaultInitLanguage,
			filepath.Base(absolute),
		)
	}
	fmt.Fprintf(stdout, "created %s (languages: %s)\n", absolute, strings.Join(selected, ", "))

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Next:")
	fmt.Fprintln(stdout, "  1. Create a TypeSafe API key: https://console.typesafe.ai/settings/keys")
	fmt.Fprintln(stdout, "  2. Export it: export TYPESAFE_API_KEY=apikey_...")
	fmt.Fprintf(stdout, "  3. Add rules to %s or install a pack.\n", filepath.Base(absolute))
	fmt.Fprintln(stdout, "  4. Run: jevlint check .")
	fmt.Fprintln(stdout, "  5. Verify setup: jevlint doctor")
	return exitSuccess
}

// detectLanguages lists the preset languages that have source files under root.
func detectLanguages(root string) []string {
	byExtension := make(map[string]string)
	for language, extensions := range parsing.PresetExtensions() {
		for _, extension := range extensions {
			byExtension[extension] = language
		}
	}
	found := make(map[string]struct{})
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && ignoredInitDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if language, ok := byExtension[strings.ToLower(filepath.Ext(path))]; ok {
			found[language] = struct{}{}
		}
		return nil
	})
	languages := make([]string, 0, len(found))
	for language := range found {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// ignoredInitDirs are directory names that language detection skips.
var ignoredInitDirs = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".venv":        {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
}

// ignoredInitDir reports whether a directory name is skipped by detection.
func ignoredInitDir(name string) bool {
	_, ok := ignoredInitDirs[name]
	return ok
}
