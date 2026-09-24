package scoping

import (
	"testing"

	"jevlint/internal/config"
)

func TestApplies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule config.Rule
		path string
		want bool
	}{
		{
			name: "default include",
			rule: config.Rule{ID: "rule"},
			path: "main.go",
			want: true,
		},
		{
			name: "recursive include",
			rule: config.Rule{ID: "rule", Include: []string{"src/**/*.ts"}},
			path: "src/services/user.ts",
			want: true,
		},
		{
			name: "not included",
			rule: config.Rule{ID: "rule", Include: []string{"src/**/*.ts"}},
			path: "test/user.ts",
			want: false,
		},
		{
			name: "excluded",
			rule: config.Rule{
				ID:      "rule",
				Include: []string{"**/*.go"},
				Exclude: []string{"**/*_test.go"},
			},
			path: "internal/user_test.go",
			want: false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Applies(test.rule, test.path)
			if err != nil {
				t.Fatalf("Applies() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Applies() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAppliesRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	_, err := Applies(config.Rule{ID: "rule", Include: []string{"["}}, "main.go")
	if err == nil {
		t.Fatal("Applies() error = nil")
	}
}
