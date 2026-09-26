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

type queryKind int

const (
	queryFunction queryKind = iota
	queryType
	queryTypeContext
)

type languageQueries map[queryKind][]string

type languagePreset struct {
	name       string
	language   *tree_sitter.Language
	extensions []string
	queries    languageQueries
	regions    map[CodeKind][]string
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
	extensions := append([]string(nil), preset.extensions...)
	if override.Extensions != nil {
		extensions = append([]string(nil), override.Extensions...)
	}
	functionQueries := append([]string(nil), preset.queries[queryFunction]...)
	if override.FunctionQueries != nil {
		functionQueries = append([]string(nil), override.FunctionQueries...)
	}
	typeQueries := append([]string(nil), preset.queries[queryType]...)
	if override.TypeQueries != nil {
		typeQueries = append([]string(nil), override.TypeQueries...)
	}
	typeContextQueries := append([]string(nil), preset.queries[queryTypeContext]...)
	if override.TypeContextQueries != nil {
		typeContextQueries = append([]string(nil), override.TypeContextQueries...)
	}
	regions := mergeRegions(map[string][]string{
		config.KindComment:   append([]string(nil), preset.regions[CodeKindComment]...),
		config.KindField:     append([]string(nil), preset.regions[CodeKindField]...),
		config.KindStatement: append([]string(nil), preset.regions[CodeKindStatement]...),
	}, override.Regions)
	languageID, ok := ParseSourceLanguage(preset.name)
	if !ok {
		return languageSpec{}, nil, fmt.Errorf("unknown language preset %q", preset.name)
	}
	regionKinds, err := categorizeRegions(regions)
	if err != nil {
		return languageSpec{}, nil, err
	}

	spec := languageSpec{
		name:             preset.name,
		id:               languageID,
		language:         preset.language,
		functionQuery:    strings.Join(functionQueries, "\n\n"),
		typeQuery:        strings.Join(typeQueries, "\n\n"),
		typeContextQuery: strings.Join(typeContextQueries, "\n\n"),
		regionKinds:      regionKinds,
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

func categorizeRegions(regions map[string][]string) (map[string]CodeKind, error) {
	result := make(map[string]CodeKind)
	for category, kinds := range regions {
		parsed, ok := ParseCodeKind(category)
		if !ok {
			return nil, fmt.Errorf("unknown region category %q", category)
		}
		for _, kind := range kinds {
			result[kind] = parsed
		}
	}
	return result, nil
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
	typescriptComments := []string{"comment"}
	typescriptFields := []string{
		"field_definition",
		"property_signature",
		"public_field_definition",
	}
	typescriptStatements := []string{
		"expression_statement",
		"lexical_declaration",
		"return_statement",
		"throw_statement",
		"variable_declaration",
	}
	return map[string]languagePreset{
		"javascript": {
			name:            "javascript",
			language:        tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
			extensions:      []string{".js", ".jsx", ".mjs", ".cjs"},
			queries: languageQueries{
				queryFunction: []string{javascriptFunctionQuery},
				queryType:     []string{javascriptTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField:   []string{"field_definition", "public_field_definition"},
				CodeKindStatement: []string{
					"expression_statement",
					"lexical_declaration",
					"return_statement",
					"throw_statement",
					"variable_declaration",
				},
			},
		},
		"typescript": {
			name:            "typescript",
			language:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
			extensions:      []string{".ts", ".mts", ".cts"},
			queries: languageQueries{
				queryFunction: []string{javascriptFunctionQuery},
				queryType:     []string{typescriptTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment:   typescriptComments,
				CodeKindField:     typescriptFields,
				CodeKindStatement: typescriptStatements,
			},
		},
		"tsx": {
			name:            "tsx",
			language:        tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
			extensions:      []string{".tsx"},
			queries: languageQueries{
				queryFunction: []string{javascriptFunctionQuery},
				queryType:     []string{typescriptTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment:   typescriptComments,
				CodeKindField:     typescriptFields,
				CodeKindStatement: typescriptStatements,
			},
		},
		"python": {
			name:            "python",
			language:        tree_sitter.NewLanguage(tree_sitter_python.Language()),
			extensions:      []string{".py"},
			queries: languageQueries{
				queryFunction: []string{pythonFunctionQuery},
				queryType:     []string{pythonTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindStatement: []string{
					"assignment",
					"assert_statement",
					"augmented_assignment",
					"expression_statement",
					"pass_statement",
					"raise_statement",
					"return_statement",
				},
			},
		},
		"go": {
			name:            "go",
			language:        tree_sitter.NewLanguage(tree_sitter_go.Language()),
			extensions:      []string{".go"},
			queries: languageQueries{
				queryFunction: []string{goFunctionQuery},
				queryType:     []string{goTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField:   []string{"field_declaration"},
				CodeKindStatement: []string{
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
			},
		},
		"rust": {
			name:               "rust",
			language:           tree_sitter.NewLanguage(tree_sitter_rust.Language()),
			extensions:         []string{".rs"},
			queries: languageQueries{
				queryFunction:   []string{rustFunctionQuery},
				queryType:       []string{rustTypeQuery},
				queryTypeContext: []string{rustImplQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"block_comment", "line_comment"},
				CodeKindField:   []string{"field_declaration"},
				CodeKindStatement: []string{
					"expression_statement",
					"let_declaration",
					"return_expression",
				},
			},
		},
		"java": {
			name:            "java",
			language:        tree_sitter.NewLanguage(tree_sitter_java.Language()),
			extensions:      []string{".java"},
			queries: languageQueries{
				queryFunction: []string{javaFunctionQuery},
				queryType:     []string{javaTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"line_comment", "block_comment"},
				CodeKindField:   []string{"field_declaration"},
				CodeKindStatement: []string{
					"assert_statement",
					"expression_statement",
					"local_variable_declaration",
					"return_statement",
					"throw_statement",
				},
			},
		},
		"csharp": {
			name:            "csharp",
			language:        tree_sitter.NewLanguage(tree_sitter_c_sharp.Language()),
			extensions:      []string{".cs"},
			queries: languageQueries{
				queryFunction: []string{csharpFunctionQuery},
				queryType:     []string{csharpTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField: []string{
					"event_field_declaration",
					"field_declaration",
					"property_declaration",
				},
				CodeKindStatement: []string{
					"expression_statement",
					"local_declaration_statement",
					"return_statement",
					"throw_statement",
					"yield_statement",
				},
			},
		},
		"ruby": {
			name:            "ruby",
			language:        tree_sitter.NewLanguage(tree_sitter_ruby.Language()),
			extensions:      []string{".rb", ".rake", ".gemspec"},
			queries: languageQueries{
				queryFunction: []string{rubyFunctionQuery},
				queryType:     []string{rubyTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment:   []string{"comment"},
				CodeKindField:     []string{"assignment", "operator_assignment"},
				CodeKindStatement: []string{"call", "return", "yield"},
			},
		},
		"php": {
			name:            "php",
			language:        tree_sitter.NewLanguage(tree_sitter_php.LanguagePHP()),
			extensions:      []string{".php", ".phtml"},
			queries: languageQueries{
				queryFunction: []string{phpFunctionQuery},
				queryType:     []string{phpTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField:   []string{"property_declaration"},
				CodeKindStatement: []string{
					"echo_statement",
					"expression_statement",
					"return_statement",
					"throw_expression",
				},
			},
		},
		"kotlin": {
			name:            "kotlin",
			language:        tree_sitter.NewLanguage(tree_sitter_kotlin.Language()),
			extensions:      []string{".kt", ".kts"},
			queries: languageQueries{
				queryFunction: []string{kotlinFunctionQuery},
				queryType:     []string{kotlinTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment:   []string{"line_comment", "multiline_comment"},
				CodeKindField:     []string{"property_declaration"},
				CodeKindStatement: []string{"assignment", "call_expression", "jump_expression"},
			},
		},
		"c": {
			name:            "c",
			language:        tree_sitter.NewLanguage(tree_sitter_c.Language()),
			extensions:      []string{".c"},
			queries: languageQueries{
				queryFunction: []string{cFunctionQuery},
				queryType:     []string{cTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField:   []string{"field_declaration"},
				CodeKindStatement: []string{
					"declaration",
					"expression_statement",
					"goto_statement",
					"return_statement",
				},
			},
		},
		"cpp": {
			name:            "cpp",
			language:        tree_sitter.NewLanguage(tree_sitter_cpp.Language()),
			extensions:      []string{".cc", ".cpp", ".cxx", ".h", ".hpp", ".hxx"},
			queries: languageQueries{
				queryFunction: []string{cppFunctionQuery},
				queryType:     []string{cppTypeQuery},
			},
			regions: map[CodeKind][]string{
				CodeKindComment: []string{"comment"},
				CodeKindField:   []string{"field_declaration"},
				CodeKindStatement: []string{
					"co_return_statement",
					"declaration",
					"expression_statement",
					"return_statement",
					"throw_statement",
				},
			},
		},
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
