package parsing

import (
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

const goImportQuery = `
(import_spec) @import
`

const javascriptImportQuery = `
(import_statement) @import
`

const pythonImportQuery = `
(import_statement) @import

(import_from_statement) @import
`

// extractImports reads the imports declared in a file, in source order.
func extractImports(spec languageSpec, source []byte, root *tree_sitter.Node) []Import {
	if spec.importQuery == nil {
		return nil
	}

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()

	importIndex, ok := namedCaptureIndex(spec.importQuery, "import")
	if !ok {
		return nil
	}

	imports := make([]Import, 0)
	matches := cursor.Matches(spec.importQuery, root, source)
	for {
		match := matches.Next()
		if match == nil {
			break
		}
		for index := range match.Captures {
			capture := &match.Captures[index]
			if capture.Index != importIndex {
				continue
			}
			node := capture.Node
			imports = append(imports, decodeImports(spec.id, &node, source)...)
		}
	}
	sort.SliceStable(imports, func(i, j int) bool {
		if imports[i].StartByte != imports[j].StartByte {
			return imports[i].StartByte < imports[j].StartByte
		}
		if imports[i].Alias != imports[j].Alias {
			return imports[i].Alias < imports[j].Alias
		}
		return imports[i].Path < imports[j].Path
	})
	return imports
}

// decodeImports turns one matched import node into imports. The grammar shapes
// differ per language, so each language has its own decoder.
func decodeImports(
	language SourceLanguage,
	node *tree_sitter.Node,
	source []byte,
) []Import {
	switch language {
	case SourceLanguageGo:
		return decodeGoImport(node, source)
	case SourceLanguageJavaScript, SourceLanguageTypeScript, SourceLanguageTSX:
		return decodeScriptImport(node, source)
	case SourceLanguagePython:
		return decodePythonImport(node, source)
	default:
		return nil
	}
}

// decodeGoImport reads one import spec.
func decodeGoImport(node *tree_sitter.Node, source []byte) []Import {
	alias := ""
	named := false
	path := ""
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		switch child.Kind() {
		case "package_identifier":
			alias = child.Utf8Text(source)
			named = true
		case "dot":
			alias = "."
			named = true
		case "blank_identifier":
			alias = "_"
			named = true
		case "interpreted_string_literal", "raw_string_literal":
			path = unquote(child.Utf8Text(source))
		}
	}
	if path == "" {
		return nil
	}
	if alias == "." || alias == "_" {
		// A dot or blank import introduces no usable qualifier.
		alias = ""
	} else if !named {
		alias = goImportPackageName(path)
	}
	return []Import{newImport(path, alias, node, source)}
}

// goImportPackageName guesses the package name from an import path: the last
// path segment, skipping a major-version segment. A wrong guess only weakens
// resolution; it is never treated as a fact.
func goImportPackageName(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if isGoVersionSegment(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	return last
}

// isGoVersionSegment reports whether a path segment is a major version like v2.
func isGoVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, r := range segment[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// decodeScriptImport reads one JavaScript or TypeScript import statement.
func decodeScriptImport(node *tree_sitter.Node, source []byte) []Import {
	pathNode := node.ChildByFieldName("source")
	if pathNode == nil {
		return nil
	}
	path := unquote(pathNode.Utf8Text(source))

	aliases := make([]string, 0)
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		if child.Kind() == "import_clause" {
			aliases = scriptImportAliases(child, source)
			break
		}
	}
	if len(aliases) == 0 {
		return []Import{newImport(path, "", node, source)}
	}
	imports := make([]Import, 0, len(aliases))
	for _, alias := range aliases {
		imports = append(imports, newImport(path, alias, node, source))
	}
	return imports
}

// scriptImportAliases lists the local names an import clause binds.
func scriptImportAliases(clause *tree_sitter.Node, source []byte) []string {
	aliases := make([]string, 0)
	for index := uint(0); index < clause.ChildCount(); index++ {
		child := clause.Child(index)
		switch child.Kind() {
		case "identifier":
			aliases = append(aliases, child.Utf8Text(source))
		case "namespace_import":
			for inner := uint(0); inner < child.ChildCount(); inner++ {
				name := child.Child(inner)
				if name.Kind() == "identifier" {
					aliases = append(aliases, name.Utf8Text(source))
					break
				}
			}
		case "named_imports":
			for inner := uint(0); inner < child.ChildCount(); inner++ {
				specifier := child.Child(inner)
				if specifier.Kind() != "import_specifier" {
					continue
				}
				if alias := specifier.ChildByFieldName("alias"); alias != nil {
					aliases = append(aliases, alias.Utf8Text(source))
					continue
				}
				if name := specifier.ChildByFieldName("name"); name != nil {
					aliases = append(aliases, name.Utf8Text(source))
				}
			}
		}
	}
	return aliases
}

// decodePythonImport reads one Python import statement.
func decodePythonImport(node *tree_sitter.Node, source []byte) []Import {
	if node.Kind() == "import_from_statement" {
		return decodePythonFromImport(node, source)
	}
	return decodePythonPlainImport(node, source)
}

// decodePythonPlainImport reads `import a`, `import a.b`, `import a.b as c`.
func decodePythonPlainImport(node *tree_sitter.Node, source []byte) []Import {
	imports := make([]Import, 0)
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		switch child.Kind() {
		case "dotted_name":
			path := child.Utf8Text(source)
			imports = append(imports, newImport(path, firstSegment(path), child, source))
		case "aliased_import":
			name := child.ChildByFieldName("name")
			alias := child.ChildByFieldName("alias")
			if name == nil || alias == nil {
				continue
			}
			imports = append(imports, newImport(
				name.Utf8Text(source),
				alias.Utf8Text(source),
				child,
				source,
			))
		}
	}
	return imports
}

// decodePythonFromImport reads `from a import b`, `from a import b as c`.
func decodePythonFromImport(node *tree_sitter.Node, source []byte) []Import {
	module := node.ChildByFieldName("module_name")
	if module == nil {
		return nil
	}
	path := module.Utf8Text(source)

	imports := make([]Import, 0)
	for index := uint(0); index < node.ChildCount(); index++ {
		child := node.Child(index)
		if child.StartByte() == module.StartByte() && child.EndByte() == module.EndByte() {
			continue
		}
		switch child.Kind() {
		case "dotted_name", "identifier":
			imports = append(imports, newImport(path, child.Utf8Text(source), child, source))
		case "aliased_import":
			alias := child.ChildByFieldName("alias")
			if alias == nil {
				continue
			}
			imports = append(imports, newImport(path, alias.Utf8Text(source), child, source))
		}
	}
	return imports
}

// firstSegment returns the text before the first dot.
func firstSegment(path string) string {
	if dot := strings.Index(path, "."); dot >= 0 {
		return path[:dot]
	}
	return path
}

// unquote removes matching surrounding quotes from a string literal.
func unquote(text string) string {
	if len(text) >= 2 {
		first := text[0]
		last := text[len(text)-1]
		if (first == '"' || first == '\'' || first == '`') && first == last {
			return text[1 : len(text)-1]
		}
	}
	return text
}

// newImport builds an import with the node's provenance.
func newImport(path, alias string, node *tree_sitter.Node, source []byte) Import {
	return Import{
		Path:      path,
		Alias:     alias,
		StartLine: node.StartPosition().Row + 1,
		EndLine:   node.EndPosition().Row + 1,
		StartByte: node.StartByte(),
		EndByte:   node.EndByte(),
		Source:    node.Utf8Text(source),
	}
}
