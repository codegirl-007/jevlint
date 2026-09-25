package config

import (
	"strings"
	"testing"
)

func TestDecodeValidConfig(t *testing.T) {
	t.Parallel()

	cfg, err := Decode(strings.NewReader(`{
		"rules": [{
			"id": "database-joins",
			"description": "Join related database records in the database.",
			"severity": "error",
			"include": ["**/*.go"],
			"kinds": ["function"],
			"localize": ["statement"]
		}]
	}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "database-joins" {
		t.Fatalf("Decode() rules = %#v", cfg.Rules)
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
			if _, err := Decode(strings.NewReader(input)); err == nil {
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
			want: `rules[0].localize contains invalid category "banana"; ` +
				"want comment, field, or statement",
		},
		"invalid code unit kind": {
			input: `{"rules": [{
				"id": "one",
				"description": "A rule.",
				"severity": "info",
				"kinds": ["banana"]
			}]}`,
			want: `rules[0].kinds contains invalid kind "banana"; want function or type`,
		},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(strings.NewReader(test.input))
			if err == nil || err.Error() != test.want {
				t.Fatalf("Decode() error = %v, want %q", err, test.want)
			}
		})
	}
}
