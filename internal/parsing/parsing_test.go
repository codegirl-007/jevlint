package parsing

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractCodeUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		path              string
		code              string
		want              []string
		contextUnit       string
		wantContextNames  []string
		wantContextSource string
	}{
		{
			name: "javascript",
			path: "sample.js",
			code: "function alpha() { return 1; }\n" +
				"const beta = () => 2;\n" +
				"class Example { gamma() { return 3; } }\n",
			want:              []string{"function:alpha", "function:beta", "type:Example", "function:gamma"},
			contextUnit:       "gamma",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "typescript",
			path: "sample.ts",
			code: "interface User { id: string }\n" +
				"type UserID = string;\n" +
				"enum Mode { Read }\n" +
				"class Service { run(user: User): UserID { return user.id; } }\n",
			want: []string{
				"type:User",
				"type:UserID",
				"type:Mode",
				"type:Service",
				"function:run",
			},
			contextUnit:       "run",
			wantContextNames:  []string{"User", "UserID", "Service"},
			wantContextSource: "class Service",
		},
		{
			name: "python",
			path: "sample.py",
			code: "def alpha():\n    return 1\n\n" +
				"class Example:\n    def beta(self):\n        return 2\n",
			want:              []string{"function:alpha", "type:Example", "function:beta"},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "go",
			path: "sample.go",
			code: "package sample\n\nfunc Alpha() int { return 1 }\n" +
				"type Example struct{}\nfunc (Example) Beta() int { return 2 }\n",
			want:              []string{"function:Alpha", "type:Example", "function:Beta"},
			contextUnit:       "Beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "type Example",
		},
		{
			name: "rust",
			path: "sample.rs",
			code: "fn alpha() -> i32 { 1 }\n" +
				"struct Example;\nimpl Example { fn beta(&self) -> i32 { 2 } }\n",
			want:              []string{"function:alpha", "type:Example", "function:beta"},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "impl Example",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			units, err := NewExtractor().Extract(test.path, []byte(test.code))
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			keys := make([]string, 0, len(units))
			for _, unit := range units {
				keys = append(keys, string(unit.Kind)+":"+unit.Name)
				if unit.StartLine == 0 || unit.EndLine < unit.StartLine {
					t.Fatalf("invalid line range in %#v", unit)
				}
			}
			if !reflect.DeepEqual(keys, test.want) {
				t.Fatalf("Extract() units = %v, want %v", keys, test.want)
			}

			var contextUnit *CodeUnit
			for index := range units {
				if units[index].Kind == CodeKindFunction &&
					units[index].Name == test.contextUnit {
					contextUnit = &units[index]
					break
				}
			}
			if contextUnit == nil {
				t.Fatalf("context unit %q not found", test.contextUnit)
			}
			contextNames := make([]string, 0, len(contextUnit.RelatedTypes))
			contextSource := ""
			for _, declaration := range contextUnit.RelatedTypes {
				contextNames = append(contextNames, declaration.Name)
				contextSource += declaration.Source
			}
			if !reflect.DeepEqual(contextNames, test.wantContextNames) {
				t.Fatalf(
					"%s context names = %v, want %v",
					test.contextUnit,
					contextNames,
					test.wantContextNames,
				)
			}
			if !strings.Contains(contextSource, test.wantContextSource) {
				t.Fatalf(
					"%s context source = %q, want it to contain %q",
					test.contextUnit,
					contextSource,
					test.wantContextSource,
				)
			}
		})
	}
}

func TestExtractRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	extractor := NewExtractor()
	if _, err := extractor.Extract("broken.go", []byte("package sample\nfunc {")); err == nil {
		t.Fatal("Extract() malformed source error = nil")
	}
	if _, err := extractor.Extract("sample.txt", []byte("not code")); err == nil {
		t.Fatal("Extract() unsupported source error = nil")
	}
}

func TestExtractIncludesLeadingDocumentationComments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		code string
		kind CodeKind
		unit string
		want string
	}{
		{
			name: "javascript function",
			path: "sample.js",
			code: "/** Explains alpha. */\nfunction alpha() {}\n",
			kind: CodeKindFunction,
			unit: "alpha",
			want: "/** Explains alpha. */",
		},
		{
			name: "typescript type",
			path: "sample.ts",
			code: "// Describes User.\ninterface User { id: string }\n",
			kind: CodeKindType,
			unit: "User",
			want: "// Describes User.",
		},
		{
			name: "decorated python function",
			path: "sample.py",
			code: "# Explains alpha.\n@decorator\ndef alpha():\n    pass\n",
			kind: CodeKindFunction,
			unit: "alpha",
			want: "# Explains alpha.\n@decorator",
		},
		{
			name: "go type",
			path: "sample.go",
			code: "package sample\n\n// User stores identity.\ntype User struct{}\n",
			kind: CodeKindType,
			unit: "User",
			want: "// User stores identity.",
		},
		{
			name: "rust function",
			path: "sample.rs",
			code: "/// Explains alpha.\nfn alpha() {}\n",
			kind: CodeKindFunction,
			unit: "alpha",
			want: "/// Explains alpha.",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			units, err := NewExtractor().Extract(test.path, []byte(test.code))
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			unit := findUnit(units, test.kind, test.unit)
			if unit == nil {
				t.Fatalf("%s %q not found in %#v", test.kind, test.unit, units)
			}
			if !strings.HasPrefix(unit.Source, test.want) {
				t.Fatalf("source = %q, want prefix %q", unit.Source, test.want)
			}
		})
	}
}

func TestExtractExcludesCommentsSeparatedByBlankLine(t *testing.T) {
	t.Parallel()

	units, err := NewExtractor().Extract(
		"sample.go",
		[]byte("package sample\n\n// Unrelated.\n\nfunc Alpha() {}\n"),
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	unit := findUnit(units, CodeKindFunction, "Alpha")
	if unit == nil {
		t.Fatal("function Alpha not found")
	}
	if strings.Contains(unit.Source, "Unrelated") {
		t.Fatalf("source includes unrelated comment: %q", unit.Source)
	}
}

func findUnit(units []CodeUnit, kind CodeKind, name string) *CodeUnit {
	for index := range units {
		if units[index].Kind == kind && units[index].Name == name {
			return &units[index]
		}
	}
	return nil
}
