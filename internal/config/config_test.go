package config

import (
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
			"kinds": ["comment", "field", "function", "statement", "type"],
			"localize": ["statement"]
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
		"empty rules": {
			input: `{"rules": []}`,
			want:  "config must contain at least one rule",
		},
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
				"severity": "unknown"
			}]}`,
			want: "rules[0].id is required",
		},
		"duplicate id takes precedence": {
			input: `{"rules": [
				{"id": "same", "description": "Valid.", "severity": "info"},
				{"id": "same", "description": "", "severity": "unknown"}
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
			want: "rules[0].severity must be info, warning, or error",
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

func withGoLanguage(input string) string {
	return strings.Replace(
		input,
		"{",
		`{"languages":{"go":{}},`,
		1,
	)
}
