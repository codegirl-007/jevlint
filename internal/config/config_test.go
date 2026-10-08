package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeValidConfig(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "database-joins",
			"description": "Join related database records in the database.",
			"severity": "error",
			"include": ["**/*.go"],
			"kinds": ["comment", "docComment", "field", "function", "statement", "type"],
			"localize": ["docComment", "statement"]
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "database-joins" {
		t.Fatalf("Decode() rules = %#v", cfg.Rules)
	}
	if cfg.MinConfidence != nil {
		t.Fatalf("Decode() minConfidence = %#v, want omitted", cfg.MinConfidence)
	}
	if cfg.Rules[0].AllowSkip || cfg.Rules[0].AllowAbstain {
		t.Fatalf("allow flags = %#v", cfg.Rules[0])
	}
	if cfg.Rules[0].Context.Callees {
		t.Fatalf("context = %#v, want omitted", cfg.Rules[0].Context)
	}
}

func TestParseTargetKindAndSeverity(t *testing.T) {
	t.Parallel()

	if kind, err := ParseTargetKind(KindFunction); err != nil || kind != TargetKindFunction {
		t.Fatalf("ParseTargetKind(%q) = %v, %v", KindFunction, kind, err)
	}
	if _, err := ParseTargetKind("banana"); err == nil || err.Error() != `invalid kind "banana"` {
		t.Fatalf("ParseTargetKind(banana) error = %v", err)
	}

	if severity, err := ParseSeverity("error"); err != nil || severity != SeverityError {
		t.Fatalf("ParseSeverity(error) = %v, %v", severity, err)
	}
	if _, err := ParseSeverity("erorr"); err == nil || err.Error() != `invalid severity "erorr"` {
		t.Fatalf("ParseSeverity(erorr) error = %v", err)
	}
}

func TestDecodeAllowsNoRules(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(`{"languages": {"go": {}}}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Rules) != 0 || len(cfg.Packs) != 0 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestDecodeRuleContext(t *testing.T) {
	t.Parallel()

	disabled, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "database-joins",
			"description": "Join related database records in the database.",
			"severity": "error",
			"context": { "callees": false }
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if disabled.Rules[0].Context.Callees {
		t.Fatalf("context.callees = %#v, want false", disabled.Rules[0].Context)
	}

	enabled, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "database-joins",
			"description": "Join related database records in the database.",
			"severity": "error",
			"kinds": ["function"],
			"context": { "callees": true }
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !enabled.Rules[0].Context.Callees {
		t.Fatalf("context.callees = %#v, want true", enabled.Rules[0].Context)
	}
}

func TestDecodeAllowSkipAndAbstain(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "database-joins",
			"description": "Join related database records in the database.",
			"severity": "error",
			"allowSkip": true,
			"allowAbstain": true
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !cfg.Rules[0].AllowSkip || !cfg.Rules[0].AllowAbstain {
		t.Fatalf("allow flags = %#v", cfg.Rules[0])
	}
}

func TestDecodeMinConfidence(t *testing.T) {
	t.Parallel()

	zero, err := Decode(strings.NewReader(withGoLanguage(`{
		"minConfidence": 0,
		"rules": [{"id": "one", "description": "A rule.", "severity": "info"}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if zero.MinConfidence == nil || *zero.MinConfidence != 0 {
		t.Fatalf("Decode() minConfidence = %#v, want 0", zero.MinConfidence)
	}

	floor, err := Decode(strings.NewReader(withGoLanguage(`{
		"minConfidence": 0.8,
		"rules": [{"id": "one", "description": "A rule.", "severity": "info"}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if floor.MinConfidence == nil || *floor.MinConfidence != 0.8 {
		t.Fatalf("Decode() minConfidence = %#v, want 0.8", floor.MinConfidence)
	}

	ruleZero, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "one",
			"description": "A rule.",
			"severity": "info",
			"minConfidence": 0
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() rule minConfidence error = %v", err)
	}
	if ruleZero.Rules[0].MinConfidence == nil || *ruleZero.Rules[0].MinConfidence != 0 {
		t.Fatalf("Decode() rule minConfidence = %#v, want 0", ruleZero.Rules[0].MinConfidence)
	}

	ruleFloor, err := Decode(strings.NewReader(withGoLanguage(`{
		"rules": [{
			"id": "one",
			"description": "A rule.",
			"severity": "info",
			"minConfidence": 0.8
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() rule minConfidence error = %v", err)
	}
	if ruleFloor.Rules[0].MinConfidence == nil || *ruleFloor.Rules[0].MinConfidence != 0.8 {
		t.Fatalf("Decode() rule minConfidence = %#v, want 0.8", ruleFloor.Rules[0].MinConfidence)
	}
}

func TestDecodeRejectsInvalidOverlaySeverity(t *testing.T) {
	t.Parallel()

	_, err := Decode(strings.NewReader(`{
		"languages": {"go": {}},
		"packs": [{"id": "database-joins", "source": "local", "sha": "abc123"}],
		"rules": [{"id": "database-joins", "severity": "erorr"}]
	}`))
	if err == nil || !strings.Contains(err.Error(), `invalid severity "erorr"`) {
		t.Fatalf("Decode() error = %v, want invalid severity", err)
	}
}

func TestDecodeAllowsOverlayWithoutSeverity(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(`{
		"languages": {"go": {}},
		"packs": [{"id": "database-joins", "source": "local", "sha": "abc123"}],
		"rules": [{"id": "database-joins", "include": ["src/**/*.go"]}]
	}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Severity != SeverityUnknown {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
}

func TestWritePartialOverlayRoundTrips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "jevlint.json")
	cfg, err := Decode(strings.NewReader(`{
		"languages": {"go": {}},
		"packs": [{"id": "database-joins", "source": "local", "sha": "abc123"}],
		"rules": [{"id": "database-joins", "include": ["src/**/*.go"]}]
	}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := Write(path, cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"severity"`) {
		t.Fatalf("written config included an unknown severity: %s", data)
	}
	reloaded, err := Decode(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("reload error = %v; data = %s", err, data)
	}
	if len(reloaded.Rules) != 1 || len(reloaded.Rules[0].Include) != 1 {
		t.Fatalf("reloaded = %#v", reloaded)
	}
}

func TestWritePreservesModeAndFormatting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "jevlint.json")
	if err := os.WriteFile(path, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Languages: map[string]Language{"go": {}},
		Rules: []Rule{{
			ID:          "one",
			Description: "A rule.",
			Severity:    SeverityError,
		}},
	}
	if err := Write(path, cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n  \"languages\"") {
		t.Fatalf("config is not indented: %s", data)
	}
	if _, err := Decode(strings.NewReader(string(data))); err != nil {
		t.Fatalf("written config does not decode: %v", err)
	}
}

func TestWriteFailureLeavesExistingConfigIntact(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "jevlint.json")
	original := "{\n  \"keep\": true\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Languages: map[string]Language{"go": {}},
		Rules: []Rule{{
			ID:          "one",
			Description: "A rule.",
			Severity:    SeverityError,
			Kinds:       []TargetKind{TargetKindUnknown},
		}},
	}
	if err := Write(path, cfg); err == nil {
		t.Fatal("Write() error = nil, want a failure")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("config was modified: %q, want %q", data, original)
	}
}

func TestWriteFailureRemovesTemporaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "jevlint.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Languages: map[string]Language{"go": {}},
		Rules: []Rule{{
			ID:          "one",
			Description: "A rule.",
			Severity:    SeverityError,
		}},
	}
	if err := Write(target, cfg); err == nil {
		t.Fatal("Write() error = nil, want a failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jevlint-config-") {
			t.Fatalf("left temporary file %q", entry.Name())
		}
	}
}
func TestConfidenceFloorPrefersRuleWhenSet(t *testing.T) {
	t.Parallel()

	global := 0.8
	ruleZero := 0.0
	cfg := Config{MinConfidence: &global}

	if got := cfg.ConfidenceFloor(Rule{}); got != 0.8 {
		t.Fatalf("omitted rule floor = %v, want global 0.8", got)
	}
	if got := cfg.ConfidenceFloor(Rule{MinConfidence: &ruleZero}); got != 0 {
		t.Fatalf("rule floor = %v, want 0", got)
	}
}

func TestDecodeRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"unknown field": `{
			"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "error",
				"unexpected": true
			}]
		}`,
		"duplicate id": `{
			"rules": [
				{"id": "same", "description": "One.", "severity": "error"},
				{"id": "same", "description": "Two.", "severity": "warning"}
			]
		}`,
		"trailing value": `{
			"rules": [{"id": "one", "description": "A rule.", "severity": "info"}]
		} {}`,
		"invalid localization category": `{
			"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"localize": ["banana"]
			}]
		}`,
		"invalid code unit kind": `{
			"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"kinds": ["banana"]
			}]
		}`,
		"unknown context field": `{
			"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"context": { "graph": true }
			}]
		}`,
	}

	for name, input := range tests {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Decode(strings.NewReader(withGoLanguage(input))); err == nil {
				t.Fatal("Decode() error = nil, want an error")
			}
		})
	}
}

func TestDecodeValidationErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  string
	}{
		"minConfidence below zero": {
			input: `{
				"minConfidence": -0.1,
				"rules": [{"id": "one", "description": "A rule.", "severity": "info"}]
			}`,
			want: "minConfidence must be between 0 and 1",
		},
		"minConfidence above one": {
			input: `{
				"minConfidence": 1.1,
				"rules": [{"id": "one", "description": "A rule.", "severity": "info"}]
			}`,
			want: "minConfidence must be between 0 and 1",
		},
		"rule minConfidence below zero": {
			input: `{
				"rules": [{
					"id": "one",
					"description": "A rule.",
					"severity": "info",
					"minConfidence": -0.1
				}]
			}`,
			want: "rules[0].minConfidence must be between 0 and 1",
		},
		"rule minConfidence above one": {
			input: `{
				"rules": [{
					"id": "one",
					"description": "A rule.",
					"severity": "info",
					"minConfidence": 1.1
				}]
			}`,
			want: "rules[0].minConfidence must be between 0 and 1",
		},
		"missing id takes precedence": {
			input: `{"rules": [{
				"id": " ",
				"description": "",
				"severity": "error"
			}]}`,
			want: "rules[0].id is required",
		},
		"duplicate id takes precedence": {
			input: `{"rules": [
				{"id": "same", "description": "Valid.", "severity": "info"},
				{"id": "same", "description": "", "severity": "error"}
			]}`,
			want: `duplicate rule id "same"`,
		},
		"missing description": {
			input: `{"rules": [{
				"id": "one",
				"description": " ",
				"severity": "error"
			}]}`,
			want: "rules[0].description is required",
		},
		"invalid severity": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "unknown"
			}]}`,
			want: `decode config: invalid severity "unknown"`,
		},
		"misspelled severity": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "erorr"
			}]}`,
			want: `decode config: invalid severity "erorr"`,
		},
		"empty include pattern": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"include": [" "]
			}]}`,
			want: "rules[0] contains an empty file pattern",
		},
		"empty exclude pattern": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"exclude": [""]
			}]}`,
			want: "rules[0] contains an empty file pattern",
		},
		"invalid localization category": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"localize": ["banana"]
			}]}`,
			want: `decode config: invalid kind "banana"`,
		},
		"invalid code unit kind": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"kinds": ["banana"]
			}]}`,
			want: `decode config: invalid kind "banana"`,
		},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(strings.NewReader(withGoLanguage(test.input)))
			if err == nil || err.Error() != test.want {
				t.Fatalf("Decode() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeLanguageOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(`{
		"languages": {
			"java": {},
			"cpp": {
				"extensions": [".cpp", ".hpp"],
				"functionQueries": ["(function_definition) @function"],
				"functionQueriesAppend": ["(lambda_expression) @function"],
				"typeQueries": ["(class_specifier) @type"],
				"regions": {
					"comment": ["comment"],
					"field": ["field_declaration"],
					"statement": ["return_statement"]
				}
			}
		},
		"rules": [{
			"id": "one",
			"description": "A rule.",
			"severity": "info"
		}]
	}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Languages) != 2 ||
		len(cfg.Languages["cpp"].Extensions) != 2 ||
		len(cfg.Languages["cpp"].FunctionQueriesAppend) != 1 ||
		cfg.Languages["java"].Extensions != nil {
		t.Fatalf("Decode() languages = %#v", cfg.Languages)
	}
}

func TestValidateRejectsInvalidLanguages(t *testing.T) {
	t.Parallel()

	validRule := []Rule{{
		ID:          "one",
		Description: "A rule.",
		Severity:    SeverityInfo,
	}}
	tests := map[string]struct {
		languages map[string]Language
		want      string
	}{
		"missing languages": {
			want: "config must enable at least one language",
		},
		"unknown preset": {
			languages: map[string]Language{"brainfuck": {}},
			want:      `languages contains unknown preset "brainfuck"`,
		},
		"empty extensions": {
			languages: map[string]Language{"go": {Extensions: []string{}}},
			want:      "languages.go.extensions cannot be empty",
		},
		"malformed extension": {
			languages: map[string]Language{"go": {Extensions: []string{"GO"}}},
			want:      `languages.go.extensions contains invalid extension "GO"`,
		},
		"duplicate extension": {
			languages: map[string]Language{
				"go":   {Extensions: []string{".source"}},
				"rust": {Extensions: []string{".source"}},
			},
			want: `language extension ".source" is assigned to both "go" and "rust"`,
		},
		"empty function queries": {
			languages: map[string]Language{
				"go": {FunctionQueries: []string{}},
			},
			want: "languages.go.functionQueries cannot be empty",
		},
		"blank type query": {
			languages: map[string]Language{
				"go": {TypeQueries: []string{" "}},
			},
			want: "languages.go.typeQueries contains an empty query",
		},
		"blank appended function query": {
			languages: map[string]Language{
				"go": {FunctionQueriesAppend: []string{" "}},
			},
			want: "languages.go.functionQueriesAppend contains an empty query",
		},
		"invalid region category": {
			languages: map[string]Language{
				"go": {Regions: map[string][]string{"banana": {"node"}}},
			},
			want: `languages.go.regions contains invalid category "banana"`,
		},
		"empty region kinds": {
			languages: map[string]Language{
				"go": {Regions: map[string][]string{"field": {}}},
			},
			want: "languages.go.regions.field cannot be empty",
		},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := (Config{
				Languages: test.languages,
				Rules:     validRule,
			}).Validate()
			if err == nil || err.Error() != test.want {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateAllowsEmptyFunctionQueriesAppend(t *testing.T) {
	t.Parallel()

	err := (Config{
		Languages: map[string]Language{"go": {FunctionQueriesAppend: []string{}}},
		Rules: []Rule{{
			ID:          "one",
			Description: "A rule.",
			Severity:    SeverityInfo,
		}},
	}).Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil for an empty append", err)
	}
}

func TestDecodePacksWithoutRules(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(withGoLanguage(`{
		"packs": [{
			"id": "database-joins",
			"source": "https://github.com/org/repo",
			"sha": "abc123"
		}]
	}`)))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Packs) != 1 || len(cfg.Rules) != 0 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestConfidenceFloorPrefersRule(t *testing.T) {
	t.Parallel()

	global := 0.8
	ruleFloor := 0.5
	cfg := Config{MinConfidence: &global}
	if cfg.ConfidenceFloor(Rule{MinConfidence: &ruleFloor}) != 0.5 {
		t.Fatalf("rule override = %v", cfg.ConfidenceFloor(Rule{MinConfidence: &ruleFloor}))
	}
	if cfg.ConfidenceFloor(Rule{}) != 0.8 {
		t.Fatalf("global floor = %v", cfg.ConfidenceFloor(Rule{}))
	}
}

func withGoLanguage(input string) string {
	return strings.Replace(
		input,
		"{",
		`{"languages":{"go":{}},`,
		1,
	)
}
