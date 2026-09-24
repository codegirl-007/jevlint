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
			"include": ["**/*.go"]
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
