package parsing

import (
	"fmt"
	"sort"
	"strings"

	tree_sitter_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_c_sharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tree_sitter_cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_php "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"jevlint/internal/config"
)

type languagePreset struct {
	name               string
	language           *tree_sitter.Language
	extensions         []string
	functionQueries    []string
	typeQueries        []string
	typeContextQueries []string
	regions            map[string][]string
}

func NewExtractor(enabled map[string]config.Language) (*Extractor, error) {
	presets := languagePresets()
	ids := make([]string, 0, len(enabled))
	for id := range enabled {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	extractor := &Extractor{byExtension: make(map[string]languageSpec)}
	for _, id := range ids {
		preset, ok := presets[id]
		if !ok {
			return nil, fmt.Errorf("unknown language preset %q", id)
		}
		spec, extensions, err := configuredLanguage(preset, enabled[id])
		if err != nil {
			return nil, err
		}
		for _, extension := range extensions {
			if existing, exists := extractor.byExtension[extension]; exists {
				return nil, fmt.Errorf(
					"language extension %q is assigned to both %q and %q",
					extension,
					existing.name,
					id,
				)
			}
			extractor.byExtension[extension] = spec
		}
	}
	return extractor, nil
}

func configuredLanguage(
	preset languagePreset,
	override config.Language,
) (languageSpec, []string, error) {
	extensions := chooseStrings(preset.extensions, override.Extensions)
	functionQueries := chooseStrings(preset.functionQueries, override.FunctionQueries)
	typeQueries := chooseStrings(preset.typeQueries, override.TypeQueries)
	typeContextQueries := chooseStrings(
		preset.typeContextQueries,
		override.TypeContextQueries,
	)
	regions := mergeRegions(preset.regions, override.Regions)

	spec := languageSpec{
		name:             preset.name,
		language:         preset.language,
		functionQuery:    strings.Join(functionQueries, "\n\n"),
		typeQuery:        strings.Join(typeQueries, "\n\n"),
		typeContextQuery: strings.Join(typeContextQueries, "\n\n"),
		regionKinds:      categorizeRegions(regions),
	}
	if err := validateConfiguredQuery(
		preset.name,
		"function",
		spec.language,
		spec.functionQuery,
		"function",
	); err != nil {
		return languageSpec{}, nil, err
	}
	if err := validateConfiguredQuery(
		preset.name,
		"type",
		spec.language,
		spec.typeQuery,
		"type",
	); err != nil {
		return languageSpec{}, nil, err
	}
	if spec.typeContextQuery != "" {
		if err := validateConfiguredQuery(
			preset.name,
			"type context",
			spec.language,
			spec.typeContextQuery,
			"type",
		); err != nil {
			return languageSpec{}, nil, err
		}
	}
	return spec, extensions, nil
}

func chooseStrings(defaults []string, override []string) []string {
	if override != nil {
		return append([]string(nil), override...)
	}
	return append([]string(nil), defaults...)
}

func mergeRegions(
	defaults map[string][]string,
	override map[string][]string,
) map[string][]string {
	result := make(map[string][]string, len(defaults)+len(override))
	for category, kinds := range defaults {
		result[category] = append([]string(nil), kinds...)
	}
	for category, kinds := range override {
		result[category] = append([]string(nil), kinds...)
	}
	return result
}

func categorizeRegions(regions map[string][]string) map[string]string {
	result := make(map[string]string)
	for category, kinds := range regions {
		for _, kind := range kinds {
			result[kind] = category
		}
	}
	return result
}

func validateConfiguredQuery(
	languageName string,
	queryName string,
	language *tree_sitter.Language,
	source string,
	targetCapture string,
) error {
	query, queryError := tree_sitter.NewQuery(language, source)
	if queryError != nil {
		return fmt.Errorf(
			"compile %s %s query: %s",
			languageName,
			queryName,
			queryError.Message,
		)
	}
	defer query.Close()

	captures := make(map[string]struct{}, len(query.CaptureNames()))
	for _, capture := range query.CaptureNames() {
		captures[capture] = struct{}{}
	}
	if _, ok := captures[targetCapture]; !ok {
		return fmt.Errorf(
			"%s %s query must capture @%s",
			languageName,
			queryName,
			targetCapture,
		)
	}
	if _, ok := captures["name"]; !ok {
		return fmt.Errorf("%s %s query must capture @name", languageName, queryName)
	}
	return nil
}

func languagePresets() map[string]languagePreset {
	return map[string]languagePreset{
		"javascript": {
			name:            "javascript",
			language:        tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
			extensions:      []string{".js", ".jsx", ".mjs", ".cjs"},
			functionQueries: []string{javascriptFunctionQuery},
			typeQueries:     []string{javascriptTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{"field_definition", "public_field_definition"},
				[]string{
					"expression_statement",
					"lexical_declaration",
					"return_statement",
					"throw_statement",
					"variable_declaration",
				},
			),
		},
		"typescript": {
			name:            "typescript",
			language:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
			extensions:      []string{".ts", ".mts", ".cts"},
			functionQueries: []string{javascriptFunctionQuery},
			typeQueries:     []string{typescriptTypeQuery},
			regions:         typescriptRegions(),
		},
		"tsx": {
			name:            "tsx",
			language:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
			extensions:      []string{".tsx"},
			functionQueries: []string{javascriptFunctionQuery},
			typeQueries:     []string{typescriptTypeQuery},
			regions:         typescriptRegions(),
		},
		"python": {
			name:            "python",
			language:        tree_sitter.NewLanguage(tree_sitter_python.Language()),
			extensions:      []string{".py"},
			functionQueries: []string{pythonFunctionQuery},
			typeQueries:     []string{pythonTypeQuery},
			regions: regions(
				[]string{"comment"},
				nil,
				[]string{
					"assignment",
					"assert_statement",
					"augmented_assignment",
					"expression_statement",
					"pass_statement",
					"raise_statement",
					"return_statement",
				},
			),
		},
		"go": {
			name:            "go",
			language:        tree_sitter.NewLanguage(tree_sitter_go.Language()),
			extensions:      []string{".go"},
			functionQueries: []string{goFunctionQuery},
			typeQueries:     []string{goTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{"field_declaration"},
				[]string{
					"assignment_statement",
					"defer_statement",
					"expression_statement",
					"go_statement",
					"inc_statement",
					"return_statement",
					"send_statement",
					"short_var_declaration",
					"var_declaration",
				},
			),
		},
		"rust": {
			name:               "rust",
			language:           tree_sitter.NewLanguage(tree_sitter_rust.Language()),
			extensions:         []string{".rs"},
			functionQueries:    []string{rustFunctionQuery},
			typeQueries:        []string{rustTypeQuery},
			typeContextQueries: []string{rustImplQuery},
			regions: regions(
				[]string{"block_comment", "line_comment"},
				[]string{"field_declaration"},
				[]string{
					"expression_statement",
					"let_declaration",
					"return_expression",
				},
			),
		},
		"java": {
			name:            "java",
			language:        tree_sitter.NewLanguage(tree_sitter_java.Language()),
			extensions:      []string{".java"},
			functionQueries: []string{javaFunctionQuery},
			typeQueries:     []string{javaTypeQuery},
			regions: regions(
				[]string{"line_comment", "block_comment"},
				[]string{"field_declaration"},
				[]string{
					"assert_statement",
					"expression_statement",
					"local_variable_declaration",
					"return_statement",
					"throw_statement",
				},
			),
		},
		"csharp": {
			name:            "csharp",
			language:        tree_sitter.NewLanguage(tree_sitter_c_sharp.Language()),
			extensions:      []string{".cs"},
			functionQueries: []string{csharpFunctionQuery},
			typeQueries:     []string{csharpTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{
					"event_field_declaration",
					"field_declaration",
					"property_declaration",
				},
				[]string{
					"expression_statement",
					"local_declaration_statement",
					"return_statement",
					"throw_statement",
					"yield_statement",
				},
			),
		},
		"ruby": {
			name:            "ruby",
			language:        tree_sitter.NewLanguage(tree_sitter_ruby.Language()),
			extensions:      []string{".rb", ".rake", ".gemspec"},
			functionQueries: []string{rubyFunctionQuery},
			typeQueries:     []string{rubyTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{"assignment", "operator_assignment"},
				[]string{"call", "return", "yield"},
			),
		},
		"php": {
			name:            "php",
			language:        tree_sitter.NewLanguage(tree_sitter_php.LanguagePHP()),
			extensions:      []string{".php", ".phtml"},
			functionQueries: []string{phpFunctionQuery},
			typeQueries:     []string{phpTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{"property_declaration"},
				[]string{
					"echo_statement",
					"expression_statement",
					"return_statement",
					"throw_expression",
				},
			),
		},
		"kotlin": {
			name:            "kotlin",
			language:        tree_sitter.NewLanguage(tree_sitter_kotlin.Language()),
			extensions:      []string{".kt", ".kts"},
			functionQueries: []string{kotlinFunctionQuery},
			typeQueries:     []string{kotlinTypeQuery},
			regions: regions(
				[]string{"line_comment", "multiline_comment"},
				[]string{"property_declaration"},
				[]string{"assignment", "call_expression", "jump_expression"},
			),
		},
		"c": {
			name:            "c",
			language:        tree_sitter.NewLanguage(tree_sitter_c.Language()),
			extensions:      []string{".c"},
			functionQueries: []string{cFunctionQuery},
			typeQueries:     []string{cTypeQuery},
			regions:         cRegions(),
		},
		"cpp": {
			name:            "cpp",
			language:        tree_sitter.NewLanguage(tree_sitter_cpp.Language()),
			extensions:      []string{".cc", ".cpp", ".cxx", ".h", ".hpp", ".hxx"},
			functionQueries: []string{cppFunctionQuery},
			typeQueries:     []string{cppTypeQuery},
			regions: regions(
				[]string{"comment"},
				[]string{"field_declaration"},
				[]string{
					"co_return_statement",
					"declaration",
					"expression_statement",
					"return_statement",
					"throw_statement",
				},
			),
		},
	}
}

func typescriptRegions() map[string][]string {
	return regions(
		[]string{"comment"},
		[]string{
			"field_definition",
			"property_signature",
			"public_field_definition",
		},
		[]string{
			"expression_statement",
			"lexical_declaration",
			"return_statement",
			"throw_statement",
			"variable_declaration",
		},
	)
}

func cRegions() map[string][]string {
	return regions(
		[]string{"comment"},
		[]string{"field_declaration"},
		[]string{
			"declaration",
			"expression_statement",
			"goto_statement",
			"return_statement",
		},
	)
}

func regions(
	comments []string,
	fields []string,
	statements []string,
) map[string][]string {
	return map[string][]string{
		"comment":   comments,
		"field":     fields,
		"statement": statements,
	}
}

const javaFunctionQuery = `
(method_declaration
  name: (identifier) @name) @function

(constructor_declaration
  name: (identifier) @name) @function

(compact_constructor_declaration
  name: (identifier) @name) @function
`

const javaTypeQuery = `
(class_declaration
  name: (identifier) @name) @type

(interface_declaration
  name: (identifier) @name) @type

(record_declaration
  name: (identifier) @name) @type

(enum_declaration
  name: (identifier) @name) @type

(annotation_type_declaration
  name: (identifier) @name) @type
`

const csharpFunctionQuery = `
(method_declaration
  name: (identifier) @name) @function

(constructor_declaration
  name: (identifier) @name) @function

(destructor_declaration
  name: (identifier) @name) @function

(local_function_statement
  name: (identifier) @name) @function
`

const csharpTypeQuery = `
(class_declaration
  name: (identifier) @name) @type

(interface_declaration
  name: (identifier) @name) @type

(struct_declaration
  name: (identifier) @name) @type

(record_declaration
  name: (identifier) @name) @type

(enum_declaration
  name: (identifier) @name) @type

(delegate_declaration
  name: (identifier) @name) @type
`

const rubyFunctionQuery = `
(method
  name: (_) @name) @function

(singleton_method
  name: (_) @name) @function
`

const rubyTypeQuery = `
(class
  name: [(constant) @name
         (scope_resolution name: (_) @name)]) @type

(singleton_class
  value: [(constant) @name
          (scope_resolution name: (_) @name)]) @type

(module
  name: [(constant) @name
         (scope_resolution name: (_) @name)]) @type
`

const phpFunctionQuery = `
(function_definition
  name: (name) @name) @function

(method_declaration
  name: (name) @name) @function
`

const phpTypeQuery = `
(class_declaration
  name: (name) @name) @type

(interface_declaration
  name: (name) @name) @type

(trait_declaration
  name: (name) @name) @type

(enum_declaration
  name: (name) @name) @type
`

const kotlinFunctionQuery = `
(function_declaration
  name: (identifier) @name) @function
`

const kotlinTypeQuery = `
(class_declaration
  name: (identifier) @name) @type

(object_declaration
  name: (identifier) @name) @type

(type_alias
  (identifier) @name) @type
`

const cFunctionQuery = `
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name)) @function

(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator
      declarator: (identifier) @name))) @function
`

const cTypeQuery = `
(struct_specifier
  name: (type_identifier) @name
  body: (_)) @type

(union_specifier
  name: (type_identifier) @name
  body: (_)) @type

(enum_specifier
  name: (type_identifier) @name
  body: (_)) @type

(type_definition
  declarator: (type_identifier) @name) @type
`

const cppFunctionQuery = `
(function_definition
  declarator: (function_declarator
    declarator: [(identifier) @name
                 (field_identifier) @name
                 (destructor_name) @name
                 (qualified_identifier name: (_) @name)])) @function

(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator
      declarator: [(identifier) @name
                   (field_identifier) @name
                   (qualified_identifier name: (_) @name)]))) @function
`

const cppTypeQuery = `
(class_specifier
  name: (type_identifier) @name) @type

(struct_specifier
  name: (type_identifier) @name
  body: (_)) @type

(union_specifier
  name: (type_identifier) @name
  body: (_)) @type

(enum_specifier
  name: (type_identifier) @name) @type

(type_definition
  declarator: (type_identifier) @name) @type

(alias_declaration
  name: (type_identifier) @name) @type
`
