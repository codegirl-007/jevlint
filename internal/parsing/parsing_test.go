package parsing

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jevlint/internal/config"
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
		{
			name: "java",
			path: "Sample.java",
			code: "class Example {\n" +
				"  Example() {}\n" +
				"  int beta() { return 2; }\n" +
				"}\n",
			want: []string{
				"type:Example",
				"function:Example",
				"function:beta",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "csharp",
			path: "Sample.cs",
			code: "class Example {\n" +
				"  public Example() {}\n" +
				"  public int Beta() { return 2; }\n" +
				"}\n",
			want: []string{
				"type:Example",
				"function:Example",
				"function:Beta",
			},
			contextUnit:       "Beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "ruby",
			path: "sample.rb",
			code: "def alpha\n  1\nend\n\n" +
				"class Example\n  def beta\n    2\n  end\nend\n",
			want: []string{
				"function:alpha",
				"type:Example",
				"function:beta",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "php",
			path: "sample.php",
			code: "<?php\nfunction alpha() { return 1; }\n" +
				"class Example { function beta() { return 2; } }\n",
			want: []string{
				"function:alpha",
				"type:Example",
				"function:beta",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "kotlin",
			path: "Sample.kt",
			code: "fun alpha() {}\n" +
				"class Example {\n  fun beta() {}\n}\n",
			want: []string{
				"function:alpha",
				"type:Example",
				"function:beta",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
		{
			name: "c",
			path: "sample.c",
			code: "struct Example { int value; };\n" +
				"int beta(struct Example value) { return value.value; }\n",
			want: []string{
				"type:Example",
				"function:beta",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "struct Example",
		},
		{
			name: "cpp",
			path: "sample.cpp",
			code: "class Example {\npublic:\n  int beta() { return 2; }\n};\n" +
				"int alpha() { return 1; }\n",
			want: []string{
				"type:Example",
				"function:beta",
				"function:alpha",
			},
			contextUnit:       "beta",
			wantContextNames:  []string{"Example"},
			wantContextSource: "class Example",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			units, err := testExtractorForPath(t, test.path).Extract(
				test.path,
				[]byte(test.code),
			)
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			keys := make([]string, 0, len(units))
			for _, unit := range units {
				keys = append(keys, unit.Kind.String()+":"+unit.Name)
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

	extractor := testExtractor(t, "go")
	if _, err := extractor.Extract("broken.go", []byte("package sample\nfunc {")); err == nil {
		t.Fatal("Extract() malformed source error = nil")
	}
	if _, err := extractor.Extract("sample.txt", []byte("not code")); err == nil {
		t.Fatal("Extract() unsupported source error = nil")
	}
}

func TestLanguagePresetsCompile(t *testing.T) {
	t.Parallel()

	presets := []string{
		"c",
		"cpp",
		"csharp",
		"go",
		"java",
		"javascript",
		"kotlin",
		"php",
		"python",
		"ruby",
		"rust",
		"tsx",
		"typescript",
	}
	for _, preset := range presets {
		preset := preset
		t.Run(preset, func(t *testing.T) {
			t.Parallel()
			testExtractor(t, preset)
		})
	}
}

func TestNewExtractorAppliesLanguageOverrides(t *testing.T) {
	t.Parallel()

	extractor, err := NewExtractor(map[string]config.Language{
		"go": {
			Extensions: []string{".golang"},
			Regions: map[string][]string{
				"statement": {"return_statement"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}
	if extractor.Supports("sample.go") || !extractor.Supports("sample.golang") {
		t.Fatalf("Extensions() = %v, want only .golang", extractor.Extensions())
	}
	units, err := extractor.Extract(
		"sample.golang",
		[]byte("package sample\n\nfunc Read() int { return 1 }\n"),
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	unit := findUnit(units, CodeKindFunction, "Read")
	if unit == nil || len(unit.Regions) != 1 ||
		unit.Regions[0].Kind != "return_statement" {
		t.Fatalf("Extract() regions = %#v", units)
	}
}

func TestNewExtractorRejectsInvalidLanguageConfiguration(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		languages map[string]config.Language
		want      string
	}{
		"extension collision": {
			languages: map[string]config.Language{
				"go":   {Extensions: []string{".rs"}},
				"rust": {},
			},
			want: `language extension ".rs" is assigned to both "go" and "rust"`,
		},
		"invalid query": {
			languages: map[string]config.Language{
				"go": {FunctionQueries: []string{"(function_declaration"}},
			},
			want: "compile go function query",
		},
		"missing target capture": {
			languages: map[string]config.Language{
				"go": {FunctionQueries: []string{
					"(function_declaration name: (identifier) @name)",
				}},
			},
			want: "go function query must capture @function",
		},
		"missing name capture": {
			languages: map[string]config.Language{
				"go": {FunctionQueries: []string{
					"(function_declaration) @function",
				}},
			},
			want: "go function query must capture @name",
		},
	}

	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := NewExtractor(test.languages)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewExtractor() error = %v, want it to contain %q", err, test.want)
			}
		})
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
		{
			name: "java type",
			path: "Sample.java",
			code: "// Describes Example.\nclass Example {}\n",
			kind: CodeKindType,
			unit: "Example",
			want: "// Describes Example.",
		},
		{
			name: "csharp type",
			path: "Sample.cs",
			code: "// Describes Example.\nclass Example {}\n",
			kind: CodeKindType,
			unit: "Example",
			want: "// Describes Example.",
		},
		{
			name: "ruby function",
			path: "sample.rb",
			code: "# Explains alpha.\ndef alpha\nend\n",
			kind: CodeKindFunction,
			unit: "alpha",
			want: "# Explains alpha.",
		},
		{
			name: "php function",
			path: "sample.php",
			code: "<?php\n// Explains alpha.\nfunction alpha() {}\n",
			kind: CodeKindFunction,
			unit: "alpha",
			want: "// Explains alpha.",
		},
		{
			name: "kotlin type",
			path: "Sample.kt",
			code: "// Describes Example.\nclass Example {}\n",
			kind: CodeKindType,
			unit: "Example",
			want: "// Describes Example.",
		},
		{
			name: "c type",
			path: "sample.c",
			code: "/** Describes Example. */\nstruct Example { int value; };\n",
			kind: CodeKindType,
			unit: "Example",
			want: "/** Describes Example. */",
		},
		{
			name: "cpp type",
			path: "sample.cpp",
			code: "// Describes Example.\nclass Example {};\n",
			kind: CodeKindType,
			unit: "Example",
			want: "// Describes Example.",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			units, err := testExtractorForPath(t, test.path).Extract(
				test.path,
				[]byte(test.code),
			)
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

	units, err := testExtractor(t, "go").Extract(
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

func TestExtractIncludesBoundedLocalizationRegions(t *testing.T) {
	t.Parallel()

	source := "package sample\n\n// FeatureFlags controls behavior.\n" +
		"type FeatureFlags struct {\n\tEnabled bool\n\tIsReady bool\n}\n"
	units, err := testExtractor(t, "go").Extract("flags.go", []byte(source))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	unit := findUnit(units, CodeKindType, "FeatureFlags")
	if unit == nil {
		t.Fatal("type FeatureFlags not found")
	}

	if len(unit.Regions) != 3 {
		t.Fatalf("regions = %#v, want comment and two fields", unit.Regions)
	}
	if unit.Regions[0].Kind != "comment" ||
		unit.Regions[0].Category != CodeKindComment ||
		unit.Regions[0].StartLine != 3 ||
		unit.Regions[0].StartColumn != 0 {
		t.Fatalf("comment region = %#v", unit.Regions[0])
	}
	if unit.Regions[1].Kind != "field_declaration" ||
		unit.Regions[1].Category != CodeKindField ||
		unit.Regions[1].Source != "Enabled bool" ||
		unit.Regions[1].StartLine != 5 ||
		unit.Regions[1].StartColumn != 1 {
		t.Fatalf("field region = %#v", unit.Regions[1])
	}
}

func TestNewLanguagePresetsCategorizeRegions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		code string
	}{
		{
			name: "java",
			path: "Sample.java",
			code: "class Example {\n" +
				"  // Controls behavior.\n" +
				"  boolean enabled;\n" +
				"  int beta() { return 1; }\n" +
				"}\n",
		},
		{
			name: "csharp",
			path: "Sample.cs",
			code: "class Example {\n" +
				"  // Controls behavior.\n" +
				"  bool Enabled;\n" +
				"  int Beta() { return 1; }\n" +
				"}\n",
		},
		{
			name: "ruby",
			path: "sample.rb",
			code: "class Example\n" +
				"  # Controls behavior.\n" +
				"  def beta\n" +
				"    @enabled = true\n" +
				"    return 1\n" +
				"  end\n" +
				"end\n",
		},
		{
			name: "php",
			path: "sample.php",
			code: "<?php\nclass Example {\n" +
				"  // Controls behavior.\n" +
				"  public bool $enabled;\n" +
				"  function beta() { return 1; }\n" +
				"}\n",
		},
		{
			name: "kotlin",
			path: "Sample.kt",
			code: "class Example {\n" +
				"  // Controls behavior.\n" +
				"  val enabled: Boolean = true\n" +
				"  fun beta() {\n" +
				"    println(enabled)\n" +
				"  }\n" +
				"}\n",
		},
		{
			name: "c",
			path: "sample.c",
			code: "struct Example {\n" +
				"  /* Controls behavior. */\n" +
				"  int enabled;\n" +
				"};\n" +
				"int beta(void) { return 1; }\n",
		},
		{
			name: "cpp",
			path: "sample.cpp",
			code: "class Example {\n" +
				"  // Controls behavior.\n" +
				"  bool enabled;\n" +
				"  int beta() { return 1; }\n" +
				"};\n",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			units, err := testExtractorForPath(t, test.path).Extract(
				test.path,
				[]byte(test.code),
			)
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			categories := make(map[CodeKind]bool)
			for _, unit := range units {
				for _, region := range unit.Regions {
					categories[region.Category] = true
				}
			}
			for _, category := range []CodeKind{CodeKindComment, CodeKindField, CodeKindStatement} {
				if !categories[category] {
					t.Errorf(
						"Extract() region categories = %v, missing %q",
						categories,
						category.String(),
					)
				}
			}
		})
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

func testExtractorForPath(t *testing.T, path string) *Extractor {
	t.Helper()

	presets := map[string]string{
		".c":    "c",
		".cpp":  "cpp",
		".cs":   "csharp",
		".go":   "go",
		".java": "java",
		".js":   "javascript",
		".kt":   "kotlin",
		".php":  "php",
		".py":   "python",
		".rb":   "ruby",
		".rs":   "rust",
		".ts":   "typescript",
	}
	preset, ok := presets[strings.ToLower(filepath.Ext(path))]
	if !ok {
		t.Fatalf("no test language preset for %q", path)
	}
	return testExtractor(t, preset)
}

func testExtractor(t *testing.T, presets ...string) *Extractor {
	t.Helper()

	enabled := make(map[string]config.Language, len(presets))
	for _, preset := range presets {
		enabled[preset] = config.Language{}
	}
	extractor, err := NewExtractor(enabled)
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}
	return extractor
}
