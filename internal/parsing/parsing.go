package parsing

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type CodeUnit struct {
	Kind         CodeKind          `json:"kind"`
	Name         string            `json:"name"`
	Language     string            `json:"language"`
	Path         string            `json:"path"`
	Source       string            `json:"source"`
	ParentSource string            `json:"parentSource,omitempty"`
	StartLine    uint              `json:"startLine"`
	EndLine      uint              `json:"endLine"`
	StartColumn  uint              `json:"startColumn"`
	EndColumn    uint              `json:"endColumn"`
	StartByte    uint              `json:"startByte"`
	EndByte      uint              `json:"endByte"`
	RelatedTypes []TypeDeclaration `json:"types,omitempty"`
	Regions      []Region          `json:"-"`
}

type CodeKind string

const (
	CodeKindFunction CodeKind = "function"
	CodeKindType     CodeKind = "type"
	CodeKindRegion   CodeKind = "region"
)

type Region struct {
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	StartLine   uint   `json:"startLine"`
	EndLine     uint   `json:"endLine"`
	StartColumn uint   `json:"startColumn"`
	EndColumn   uint   `json:"endColumn"`
	StartByte   uint   `json:"startByte"`
	EndByte     uint   `json:"endByte"`
}

type TypeDeclaration struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	StartLine uint   `json:"startLine"`
	EndLine   uint   `json:"endLine"`
	StartByte uint   `json:"startByte"`
	EndByte   uint   `json:"endByte"`
}

type languageSpec struct {
	name             string
	language         *tree_sitter.Language
	functionQuery    string
	typeQuery        string
	typeContextQuery string
	regionKinds      map[string]string
}

type Extractor struct {
	byExtension map[string]languageSpec
}

func (extractor *Extractor) Supports(path string) bool {
	_, ok := extractor.byExtension[strings.ToLower(filepath.Ext(path))]
	return ok
}

func (extractor *Extractor) Extensions() []string {
	extensions := make([]string, 0, len(extractor.byExtension))
	for extension := range extractor.byExtension {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	return extensions
}

func (extractor *Extractor) Extract(path string, source []byte) ([]CodeUnit, error) {
	spec, ok := extractor.byExtension[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, fmt.Errorf("unsupported source file %q", path)
	}

	tree, err := parseSource(spec, path, source)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	functions, types, err := extractPrimaryUnits(spec, path, source, root)
	if err != nil {
		return nil, err
	}
	declarations, err := extractTypeDeclarations(spec, path, source, root, types)
	if err != nil {
		return nil, err
	}
	attachRelatedTypes(functions, declarations)

	units := append(functions, types...)
	attachRegions(units, extractRegions(root, source, spec.regionKinds))
	sortCodeUnits(units)
	return units, nil
}

func parseSource(
	spec languageSpec,
	path string,
	source []byte,
) (*tree_sitter.Tree, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(spec.language); err != nil {
		return nil, fmt.Errorf("set %s grammar: %w", spec.name, err)
	}

	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %q: parser returned no syntax tree", path)
	}

	root := tree.RootNode()
	if root.HasError() {
		return nil, fmt.Errorf("parse %q: source contains syntax errors", path)
	}
	return tree, nil
}

func extractPrimaryUnits(
	spec languageSpec,
	path string,
	source []byte,
	root *tree_sitter.Node,
) ([]CodeUnit, []CodeUnit, error) {
	functions, err := extractMatches(
		spec,
		path,
		source,
		root,
		spec.functionQuery,
		"function",
		CodeKindFunction,
	)
	if err != nil {
		return nil, nil, err
	}
	types, err := extractMatches(
		spec,
		path,
		source,
		root,
		spec.typeQuery,
		"type",
		CodeKindType,
	)
	if err != nil {
		return nil, nil, err
	}
	return functions, types, nil
}

func extractTypeDeclarations(
	spec languageSpec,
	path string,
	source []byte,
	root *tree_sitter.Node,
	types []CodeUnit,
) ([]TypeDeclaration, error) {
	declarations := typeDeclarations(types)
	if spec.typeContextQuery != "" {
		contextTypes, err := extractMatches(
			spec,
			path,
			source,
			root,
			spec.typeContextQuery,
			"type",
			CodeKindType,
		)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, typeDeclarations(contextTypes)...)
	}
	return declarations, nil
}

func typeDeclarations(units []CodeUnit) []TypeDeclaration {
	declarations := make([]TypeDeclaration, 0, len(units))
	for _, unit := range units {
		declarations = append(declarations, TypeDeclaration{
			Name:      unit.Name,
			Source:    unit.Source,
			StartLine: unit.StartLine,
			EndLine:   unit.EndLine,
			StartByte: unit.StartByte,
			EndByte:   unit.EndByte,
		})
	}
	return declarations
}

func attachRelatedTypes(functions []CodeUnit, declarations []TypeDeclaration) {
	for index := range functions {
		for _, declaration := range declarations {
			if declarationContains(declaration, functions[index]) ||
				containsIdentifier(functions[index].Source, declaration.Name) {
				functions[index].RelatedTypes = append(
					functions[index].RelatedTypes,
					declaration,
				)
			}
		}
	}
}

func attachRegions(units []CodeUnit, regions []Region) {
	for index := range units {
		for _, region := range regions {
			if region.StartByte < units[index].StartByte ||
				region.EndByte > units[index].EndByte {
				continue
			}
			units[index].Regions = append(units[index].Regions, region)
			if len(units[index].Regions) == 24 {
				break
			}
		}
	}
}

func sortCodeUnits(units []CodeUnit) {
	sort.SliceStable(units, func(i, j int) bool {
		if units[i].StartByte == units[j].StartByte {
			return units[i].Kind == CodeKindType
		}
		return units[i].StartByte < units[j].StartByte
	})
}

func extractMatches(
	spec languageSpec,
	path string,
	source []byte,
	root *tree_sitter.Node,
	querySource string,
	captureName string,
	kind CodeKind,
) ([]CodeUnit, error) {
	query, queryError := tree_sitter.NewQuery(spec.language, querySource)
	if queryError != nil {
		return nil, fmt.Errorf("compile %s %s query: %s", spec.name, kind, queryError.Message)
	}
	defer query.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()

	matches := cursor.Matches(query, root, source)
	units := make([]CodeUnit, 0)
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var unitNode, nameNode *tree_sitter.Node
		for index := range match.Captures {
			capture := &match.Captures[index]
			switch query.CaptureNames()[capture.Index] {
			case captureName:
				node := capture.Node
				unitNode = &node
			case "name":
				node := capture.Node
				nameNode = &node
			}
		}
		if unitNode == nil || nameNode == nil {
			continue
		}

		sourceNode := documentationAnchor(unitNode)
		sourceStartByte, sourceStartPosition := leadingCommentStart(sourceNode, source)
		end := unitNode.EndPosition()
		units = append(units, CodeUnit{
			Kind:        kind,
			Name:        nameNode.Utf8Text(source),
			Language:    spec.name,
			Path:        path,
			Source:      string(source[sourceStartByte:unitNode.EndByte()]),
			StartLine:   sourceStartPosition.Row + 1,
			EndLine:     end.Row + 1,
			StartColumn: sourceStartPosition.Column,
			EndColumn:   end.Column,
			StartByte:   sourceStartByte,
			EndByte:     unitNode.EndByte(),
		})
	}
	return units, nil
}

func extractRegions(
	root *tree_sitter.Node,
	source []byte,
	kinds map[string]string,
) []Region {
	regions := make([]Region, 0)
	var walk func(*tree_sitter.Node)
	walk = func(node *tree_sitter.Node) {
		if category, ok := kinds[node.Kind()]; ok {
			start := node.StartPosition()
			end := node.EndPosition()
			regions = append(regions, Region{
				Category:    category,
				Kind:        node.Kind(),
				Source:      node.Utf8Text(source),
				StartLine:   start.Row + 1,
				EndLine:     end.Row + 1,
				StartColumn: start.Column,
				EndColumn:   end.Column,
				StartByte:   node.StartByte(),
				EndByte:     node.EndByte(),
			})
		}
		for index := uint(0); index < node.NamedChildCount(); index++ {
			child := node.NamedChild(index)
			if child != nil {
				walk(child)
			}
		}
	}
	walk(root)
	sort.SliceStable(regions, func(i, j int) bool {
		if regions[i].StartByte == regions[j].StartByte {
			return regions[i].EndByte < regions[j].EndByte
		}
		return regions[i].StartByte < regions[j].StartByte
	})
	return regions
}

func documentationAnchor(node *tree_sitter.Node) *tree_sitter.Node {
	parent := node.Parent()
	if parent != nil && parent.Kind() == "decorated_definition" {
		return parent
	}
	return node
}

func leadingCommentStart(
	node *tree_sitter.Node,
	source []byte,
) (uint, tree_sitter.Point) {
	startByte := node.StartByte()
	startPosition := node.StartPosition()

	for previous := node.PrevNamedSibling(); previous != nil; previous = previous.PrevNamedSibling() {
		if !isCommentNode(previous.Kind()) ||
			!isAdjacentCommentGap(source[previous.EndByte():startByte]) {
			break
		}
		startByte = previous.StartByte()
		startPosition = previous.StartPosition()
	}
	return startByte, startPosition
}

func isCommentNode(kind string) bool {
	return kind == "comment" || strings.HasSuffix(kind, "comment")
}

func isAdjacentCommentGap(gap []byte) bool {
	newlines := 0
	for _, value := range gap {
		switch value {
		case '\n':
			newlines++
			if newlines > 1 {
				return false
			}
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

func declarationContains(declaration TypeDeclaration, unit CodeUnit) bool {
	return declaration.StartByte <= unit.StartByte && declaration.EndByte >= unit.EndByte
}

func containsIdentifier(source string, identifier string) bool {
	for index := 0; index < len(source); {
		start := strings.Index(source[index:], identifier)
		if start < 0 {
			return false
		}
		start += index
		end := start + len(identifier)
		beforeBoundary := start == 0 || !isIdentifierByte(source[start-1])
		afterBoundary := end == len(source) || !isIdentifierByte(source[end])
		if beforeBoundary && afterBoundary {
			return true
		}
		index = end
	}
	return false
}

func isIdentifierByte(value byte) bool {
	return value == '_' ||
		value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}

const javascriptFunctionQuery = `
(function_declaration
  name: (identifier) @name) @function

(generator_function_declaration
  name: (identifier) @name) @function

(method_definition
  name: [(property_identifier) (private_property_identifier)] @name) @function

(variable_declarator
  name: (identifier) @name
  value: [(arrow_function) (function_expression)] @function)
`

const javascriptTypeQuery = `
(class_declaration
  name: (identifier) @name) @type
`

const typescriptTypeQuery = `
(class_declaration
  name: (type_identifier) @name) @type

(abstract_class_declaration
  name: (type_identifier) @name) @type

(interface_declaration
  name: (type_identifier) @name) @type

(type_alias_declaration
  name: (type_identifier) @name) @type

(enum_declaration
  name: (identifier) @name) @type
`

const pythonFunctionQuery = `
(function_definition
  name: (identifier) @name) @function
`

const pythonTypeQuery = `
(class_definition
  name: (identifier) @name) @type
`

const goFunctionQuery = `
(function_declaration
  name: (identifier) @name) @function

(method_declaration
  name: (field_identifier) @name) @function
`

const goTypeQuery = `
(type_declaration
  (type_spec
    name: (type_identifier) @name)) @type

(type_declaration
  (type_alias
    name: (type_identifier) @name)) @type
`

const rustFunctionQuery = `
(function_item
  name: (identifier) @name) @function
`

const rustTypeQuery = `
(struct_item
  name: (type_identifier) @name) @type

(enum_item
  name: (type_identifier) @name) @type

(union_item
  name: (type_identifier) @name) @type

(trait_item
  name: (type_identifier) @name) @type

(type_item
  name: (type_identifier) @name) @type
`

const rustImplQuery = `
(impl_item
  type: (type_identifier) @name) @type
`
