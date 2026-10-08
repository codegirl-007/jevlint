package parsing

import (
	"reflect"
	"strings"
	"testing"

	"github.com/codegirl-007/jevlint/internal/config"
)

func TestExtractJavaScriptFunctionShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want []string
	}{
		{
			name: "function declaration",
			code: "function foo() {}",
			want: []string{"foo"},
		},
		{
			name: "generator function declaration",
			code: "function* foo() {}",
			want: []string{"foo"},
		},
		{
			name: "class methods",
			code: "class Example {\n  foo() {}\n  #privateFoo() {}\n}\n",
			want: []string{"foo", "#privateFoo"},
		},
		{
			name: "arrow assigned to a variable",
			code: "const foo = () => {};",
			want: []string{"foo"},
		},
		{
			name: "function expression assigned to a variable",
			code: "const foo = function () {};",
			want: []string{"foo"},
		},
		{
			name: "generator function expression assigned to a variable",
			code: "const foo = function* () {};",
			want: []string{"foo"},
		},
		{
			name: "callback passed to a wrapper",
			code: "const foo = wrapper(() => {});",
			want: []string{"foo"},
		},
		{
			name: "async callback passed to a wrapper",
			code: "const foo = customWrapper(async () => {});",
			want: []string{"foo"},
		},
		{
			name: "test callback",
			code: `it("does the thing", () => {});`,
			want: []string{"does the thing"},
		},
		{
			name: "async test callback",
			code: `test("does the thing", async () => {});`,
			want: []string{"does the thing"},
		},
		{
			name: "focused test callback",
			code: `it.only("does the thing", () => {});`,
			want: []string{"does the thing"},
		},
		{
			name: "skipped test callback",
			code: `test.skip("does the thing", () => {});`,
			want: []string{"does the thing"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := extractFunctionNames(t, "sample.ts", test.code)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("function names = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestExtractJavaScriptCallbackSourceIsTheImplementation(t *testing.T) {
	t.Parallel()

	unit := functionNamed(t, extractFromSource(t, "sample.ts",
		"const foo = wrapper(() => {\n  return 1;\n});\n"), "foo")
	if strings.Contains(unit.Source, "wrapper") {
		t.Fatalf("source = %q, want the callback implementation only", unit.Source)
	}
	if !strings.Contains(unit.Source, "return 1") {
		t.Fatalf("source = %q, want the callback body", unit.Source)
	}
}

func TestExtractJavaScriptIgnoresBareCallbacks(t *testing.T) {
	t.Parallel()

	code := "items.map((item) => item.name);\n" +
		"promise.then(() => {});\n" +
		"button.addEventListener(\"click\", () => {});\n"
	if got := extractFunctionNames(t, "sample.ts", code); len(got) != 0 {
		t.Fatalf("function names = %#v, want none", got)
	}
}

func TestExtractJavaScriptDeduplicatesMatchingPatterns(t *testing.T) {
	t.Parallel()

	units := extractFromSource(t, "sample.ts",
		`const foo = test("does the thing", () => {});`)

	functions := functionsIn(units)
	if len(functions) != 1 {
		t.Fatalf("functions = %#v, want one deduplicated unit", functionNames(functions))
	}
	ranges := make(map[unitRange]struct{}, len(units))
	for _, unit := range units {
		key := unitRange{start: unit.StartByte, end: unit.EndByte}
		if _, exists := ranges[key]; exists {
			t.Fatalf("duplicate unit range %#v in %#v", key, units)
		}
		ranges[key] = struct{}{}
	}
}

func TestCustomFunctionQueriesReplaceDefaults(t *testing.T) {
	t.Parallel()

	extractor, err := NewExtractor(map[string]config.Language{
		"javascript": {
			FunctionQueries: []string{
				"(function_declaration\n  name: (identifier) @name) @function\n",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	units, err := extractor.Extract("sample.js", []byte(
		"function foo() {}\nconst bar = () => {};\n",
	))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got := functionNames(functionsIn(units)); !reflect.DeepEqual(got, []string{"foo"}) {
		t.Fatalf("function names = %#v, want only the custom query match", got)
	}
}

func TestCustomFunctionQueriesDeduplicateWithDefaults(t *testing.T) {
	t.Parallel()

	extractor, err := NewExtractor(map[string]config.Language{
		"javascript": {
			FunctionQueries: []string{
				"(function_declaration\n  name: (identifier) @name) @function\n",
				"(function_declaration\n  name: (identifier) @name) @function\n",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	units, err := extractor.Extract("sample.js", []byte("function foo() {}"))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got := functionNames(functionsIn(units)); !reflect.DeepEqual(got, []string{"foo"}) {
		t.Fatalf("function names = %#v, want one deduplicated unit", got)
	}
}

func extractFunctionNames(t *testing.T, path string, source string) []string {
	t.Helper()
	return functionNames(functionsIn(extractFromSource(t, path, source)))
}

func extractFromSource(t *testing.T, path string, source string) []CodeUnit {
	t.Helper()

	units, err := testExtractorForPath(t, path).Extract(path, []byte(source))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	return units
}

func functionsIn(units []CodeUnit) []CodeUnit {
	functions := make([]CodeUnit, 0)
	for _, unit := range units {
		if unit.Kind == CodeKindFunction {
			functions = append(functions, unit)
		}
	}
	return functions
}

func functionNames(functions []CodeUnit) []string {
	names := make([]string, 0, len(functions))
	for _, function := range functions {
		names = append(names, function.Name)
	}
	return names
}

func TestFunctionQueriesAppendAddsToDefaults(t *testing.T) {
	t.Parallel()

	// `describe` is not in the built-in query, so it can only come from the append.
	extractor, err := NewExtractor(map[string]config.Language{
		"typescript": {
			FunctionQueriesAppend: []string{
				"(call_expression function: (identifier) @_fn arguments: (arguments " +
					". (string (string_fragment) @name) . " +
					"[(arrow_function) (function_expression)] @function) " +
					"(#any-of? @_fn \"describe\"))",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	units, err := extractor.Extract("sample.ts", []byte(
		"function bar() {}\ndescribe(\"suite\", () => {});\n",
	))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got := functionNames(functionsIn(units)); !reflect.DeepEqual(got, []string{"bar", "suite"}) {
		t.Fatalf("function names = %#v, want [bar suite]", got)
	}
}

func TestFunctionQueriesAppendComposesWithReplace(t *testing.T) {
	t.Parallel()

	extractor, err := NewExtractor(map[string]config.Language{
		"typescript": {
			FunctionQueries: []string{
				"(function_declaration\n  name: (identifier) @name) @function",
			},
			FunctionQueriesAppend: []string{
				"(variable_declarator\n  name: (identifier) @name\n" +
					"  value: [(arrow_function) (function_expression)] @function)",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	units, err := extractor.Extract("sample.ts", []byte(
		"function bar() {}\nconst foo = () => {};\nclass C {\n  m() {}\n}\n",
	))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	// Replace drops the method pattern; append adds the arrow pattern.
	if got := functionNames(functionsIn(units)); !reflect.DeepEqual(got, []string{"bar", "foo"}) {
		t.Fatalf("function names = %#v, want [bar foo]", got)
	}
}

func TestFunctionQueriesAppendEmptyIsNoOp(t *testing.T) {
	t.Parallel()

	extractor, err := NewExtractor(map[string]config.Language{
		"typescript": {FunctionQueriesAppend: []string{}},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	source := "function bar() {}\nconst foo = () => {};\nclass C {\n  m() {}\n}\n"
	units, err := extractor.Extract("sample.ts", []byte(source))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got := functionNames(functionsIn(units)); !reflect.DeepEqual(got, []string{"bar", "foo", "m"}) {
		t.Fatalf("function names = %#v, want the built-in defaults", got)
	}
}
