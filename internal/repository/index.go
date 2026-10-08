// Package repository builds a deterministic, conservative index of the
// relationships Jevlint can establish from parsed source: which functions call
// which, who calls them, what type contains a method, which types relate to a
// function, and which imports a unit references. It never guesses: a
// relationship that cannot be resolved is simply absent.
package repository

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/codegirl-007/jevlint/internal/parsing"
)

const (
	maxDirectCallees     = 12
	maxCalleeSourceBytes = 16 << 10
)

// SymbolID uniquely identifies one function or type.
type SymbolID string

// Symbol is one function or type in the index.
type Symbol struct {
	ID        SymbolID
	Name      string
	Path      string
	Language  parsing.SourceLanguage
	Kind      parsing.CodeKind
	StartLine uint
	EndLine   uint
	StartByte uint
	EndByte   uint
	Source    string
}

// RepositoryIndex answers repository relationship questions for code units.
type RepositoryIndex struct {
	symbols   map[SymbolID]Symbol
	byName    map[string][]SymbolID
	byDir     map[string][]SymbolID
	files     map[string][]SymbolID
	calls     map[SymbolID][]SymbolID
	callRefs  map[SymbolID][]parsing.CallRef
	callers   map[SymbolID][]SymbolID
	contained map[SymbolID]SymbolID
	related   map[SymbolID][]parsing.TypeDeclaration
	imports   map[string][]parsing.Import
	// modulePath is the Go module path (from go.mod), used to decide whether a
	// non-relative import names code in this repository.
	modulePath string
}

// New builds an index from the extracted files.
func New(files []parsing.FileExtraction, modulePath string) *RepositoryIndex {
	index := &RepositoryIndex{
		modulePath: modulePath,
		symbols:    make(map[SymbolID]Symbol),
		byName:     make(map[string][]SymbolID),
		byDir:      make(map[string][]SymbolID),
		files:      make(map[string][]SymbolID),
		calls:      make(map[SymbolID][]SymbolID),
		callRefs:   make(map[SymbolID][]parsing.CallRef),
		callers:    make(map[SymbolID][]SymbolID),
		contained:  make(map[SymbolID]SymbolID),
		related:    make(map[SymbolID][]parsing.TypeDeclaration),
		imports:    make(map[string][]parsing.Import),
	}
	index.addSymbols(files)
	index.addImports(files)
	index.addContainment()
	index.addCalls(files)
	index.addRelated(files)
	return index
}

// addSymbols registers every function and type unit.
func (index *RepositoryIndex) addSymbols(files []parsing.FileExtraction) {
	for _, file := range files {
		for _, unit := range file.Units {
			if unit.Kind != parsing.CodeKindFunction && unit.Kind != parsing.CodeKindType {
				continue
			}
			symbol := Symbol{
				ID:        symbolID(unit),
				Name:      unit.Name,
				Path:      unit.Path,
				Language:  unit.Language,
				Kind:      unit.Kind,
				StartLine: unit.StartLine,
				EndLine:   unit.EndLine,
				StartByte: unit.StartByte,
				EndByte:   unit.EndByte,
				Source:    unit.Source,
			}
			index.symbols[symbol.ID] = symbol
			index.files[unit.Path] = append(index.files[unit.Path], symbol.ID)
			index.byDir[path.Dir(unit.Path)] = append(index.byDir[path.Dir(unit.Path)], symbol.ID)
			if unit.Kind == parsing.CodeKindFunction {
				index.byName[unit.Name] = append(index.byName[unit.Name], symbol.ID)
			}
		}
	}
	for path := range index.files {
		index.files[path] = sortedSymbols(index, index.files[path])
	}
	for name := range index.byName {
		sort.SliceStable(index.byName[name], func(i, j int) bool {
			return lessSymbol(index.symbols[index.byName[name][i]], index.symbols[index.byName[name][j]])
		})
	}
}

// symbolID uniquely names a unit by path, kind, name, and start position.
func symbolID(unit parsing.CodeUnit) SymbolID {
	return SymbolID(
		unit.Path + "#" + unit.Kind.String() + "#" + unit.Name + "#" +
			strconv.FormatUint(uint64(unit.StartByte), 10),
	)
}

// addContainment records the type that contains each method.
func (index *RepositoryIndex) addContainment() {
	for id, symbol := range index.symbols {
		if symbol.Kind != parsing.CodeKindFunction {
			continue
		}
		if containing, ok := index.containingTypeFor(symbol); ok {
			index.contained[id] = containing
		}
	}
}

// containingTypeFor returns the type that contains a method. It uses lexical
// containment where the grammar nests the method, and the explicit receiver for
// Go, where methods are declared outside their type.
func (index *RepositoryIndex) containingTypeFor(symbol Symbol) (SymbolID, bool) {
	if containing, ok := index.smallestTypeContaining(symbol); ok {
		return containing, true
	}
	if symbol.Language != parsing.SourceLanguageGo {
		return "", false
	}
	receiver, ok := goReceiverType(symbol.Source)
	if !ok {
		return "", false
	}
	return index.uniqueTypeNamed(symbol.Path, receiver)
}

// uniqueTypeNamed returns the one type symbol with a name in a file.
func (index *RepositoryIndex) uniqueTypeNamed(path string, name string) (SymbolID, bool) {
	found := SymbolID("")
	matches := 0
	for _, id := range index.files[path] {
		candidate := index.symbols[id]
		if candidate.Kind == parsing.CodeKindType && candidate.Name == name {
			found = id
			matches++
		}
	}
	if matches != 1 {
		return "", false
	}
	return found, true
}

// goReceiverType reads the receiver type name from a Go method's source. It
// returns false for anything that is not a plain `func (recv Type)` form.
func goReceiverType(source string) (string, bool) {
	trimmed := strings.TrimLeft(stripLeadingComments(source), " \t\r\n")
	const prefix = "func ("
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	rest := trimmed[len(prefix):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) == 0 {
		return "", false
	}
	receiver := fields[len(fields)-1]
	receiver = strings.TrimPrefix(receiver, "*")
	if dot := strings.LastIndexByte(receiver, '.'); dot >= 0 {
		receiver = receiver[dot+1:]
	}
	if bracket := strings.IndexByte(receiver, '['); bracket >= 0 {
		receiver = receiver[:bracket]
	}
	if receiver == "" {
		return "", false
	}
	return receiver, true
}

// stripLeadingComments removes the line or block comment a declaration's source
// can begin with, so a method's receiver can be read from its `func` line.
func stripLeadingComments(source string) string {
	rest := source
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		switch {
		case strings.HasPrefix(rest, "//"):
			newline := strings.IndexByte(rest, '\n')
			if newline < 0 {
				return ""
			}
			rest = rest[newline+1:]
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest, "*/")
			if end < 0 {
				return ""
			}
			rest = rest[end+2:]
		default:
			return rest
		}
	}
}

// smallestTypeContaining returns the smallest type symbol that contains a unit.
func (index *RepositoryIndex) smallestTypeContaining(symbol Symbol) (SymbolID, bool) {
	var best SymbolID
	found := false
	for _, candidateID := range index.files[symbol.Path] {
		candidate := index.symbols[candidateID]
		if candidate.Kind != parsing.CodeKindType {
			continue
		}
		if candidate.StartByte > symbol.StartByte || candidate.EndByte < symbol.EndByte {
			continue
		}
		if candidate.StartByte == symbol.StartByte && candidate.EndByte == symbol.EndByte {
			continue
		}
		if !found || lessSpan(candidate, index.symbols[best]) {
			best = candidateID
			found = true
		}
	}
	return best, found
}

// addCalls resolves every syntactic call to a callee, where it can be done
// conservatively, and records the resolved edges per caller.
func (index *RepositoryIndex) addCalls(files []parsing.FileExtraction) {
	type assignment struct {
		caller SymbolID
		ref    parsing.CallRef
	}
	assigned := make([]assignment, 0)
	for _, file := range files {
		functions := index.fileFunctions(file.Path)
		for _, call := range file.Calls {
			caller, ok := innermostFunction(index, functions, call)
			if !ok {
				continue
			}
			assigned = append(assigned, assignment{caller: caller, ref: call.Ref})
		}
	}

	callersSeen := make(map[SymbolID]bool)
	callers := make([]SymbolID, 0)
	grouped := make(map[SymbolID][]parsing.CallRef)
	for _, item := range assigned {
		if !callersSeen[item.caller] {
			callersSeen[item.caller] = true
			callers = append(callers, item.caller)
		}
		grouped[item.caller] = append(grouped[item.caller], item.ref)
	}

	for _, callerID := range callers {
		caller := index.symbols[callerID]
		refs := dedupeSpellings(grouped[callerID])
		index.callRefs[callerID] = refs
		resolved := make([]Symbol, 0, len(refs))
		seen := make(map[SymbolID]struct{}, len(refs))
		for _, ref := range refs {
			target, ok := index.resolveCall(caller, ref)
			if !ok {
				continue
			}
			if _, exists := seen[target.ID]; exists {
				continue
			}
			seen[target.ID] = struct{}{}
			resolved = append(resolved, target)
		}
		if len(resolved) == 0 {
			continue
		}
		index.calls[callerID] = symbolIDs(resolved)
	}

	// Process callers in deterministic order so the reverse edges are stable.
	sortSymbolsByID(index, callers)
	for _, callerID := range callers {
		for _, targetID := range index.calls[callerID] {
			index.callers[targetID] = appendUnique(index.callers[targetID], callerID)
		}
	}
}

// resolveCall finds the one function a call can mean. Unqualified calls use
// same-file and project-unique names; qualified calls need import evidence.
func (index *RepositoryIndex) resolveCall(caller Symbol, ref parsing.CallRef) (Symbol, bool) {
	if ref.Path != "" {
		return index.resolveQualified(caller, ref)
	}
	candidates := index.byName[ref.Name]
	if len(candidates) == 0 {
		return Symbol{}, false
	}
	sameFile := make([]SymbolID, 0, len(candidates))
	for _, id := range candidates {
		if index.symbols[id].Path == caller.Path {
			sameFile = append(sameFile, id)
		}
	}
	if len(sameFile) == 1 {
		return index.symbols[sameFile[0]], true
	}
	if len(sameFile) > 1 {
		return Symbol{}, false
	}
	if len(candidates) == 1 {
		return index.symbols[candidates[0]], true
	}
	return Symbol{}, false
}

// resolveQualified binds a qualified call only when an import alias in the
// caller's file points at a package directory holding exactly one function with
// the called name. Anything ambiguous stays unresolved.
func (index *RepositoryIndex) resolveQualified(
	caller Symbol,
	ref parsing.CallRef,
) (Symbol, bool) {
	qualifier := firstSegment(ref.Path)
	candidates := make([]SymbolID, 0)
	seen := make(map[SymbolID]struct{})
	for _, imported := range index.imports[caller.Path] {
		if imported.Alias != qualifier {
			continue
		}
		for _, dir := range index.importDirectories(caller.Path, imported.Path) {
			for _, id := range index.byDir[dir] {
				symbol := index.symbols[id]
				if symbol.Kind != parsing.CodeKindFunction || symbol.Name != ref.Name {
					continue
				}
				if _, exists := seen[id]; exists {
					continue
				}
				seen[id] = struct{}{}
				candidates = append(candidates, id)
			}
		}
	}
	if len(candidates) != 1 {
		return Symbol{}, false
	}
	return index.symbols[candidates[0]], true
}

// importDirectories maps an import path to the directories it could name. A
// relative path resolves against the importing file; a non-relative path only
// resolves when it is inside the repository's module.
func (index *RepositoryIndex) importDirectories(callerPath, importPath string) []string {
	if strings.HasPrefix(importPath, ".") {
		return []string{path.Clean(path.Join(path.Dir(callerPath), importPath))}
	}
	// A module import can only name code in this repository when it sits inside
	// the repository's module. Without a known module path, refuse to guess:
	// suffix matching would let an unrelated module such as
	// "github.com/other/project/internal/auth" bind to a local internal/auth.
	if index.modulePath == "" {
		return nil
	}
	dir := ""
	switch {
	case importPath == index.modulePath:
		dir = "."
	case strings.HasPrefix(importPath, index.modulePath+"/"):
		dir = strings.TrimPrefix(importPath, index.modulePath+"/")
	default:
		return nil
	}
	if _, ok := index.byDir[dir]; !ok {
		return nil
	}
	return []string{dir}
}

// addRelated links each function to the type declarations it mentions, using
// the same conservative containment and whole-word rules as before.
func (index *RepositoryIndex) addRelated(files []parsing.FileExtraction) {
	for _, file := range files {
		declarations := file.TypeDeclarations
		if len(declarations) == 0 {
			continue
		}
		for _, id := range index.files[file.Path] {
			symbol := index.symbols[id]
			if symbol.Kind != parsing.CodeKindFunction {
				continue
			}
			for _, declaration := range declarations {
				if declarationContains(declaration, symbol) ||
					containsIdentifier(symbol.Source, declaration.Name) {
					index.related[id] = append(index.related[id], declaration)
				}
			}
		}
	}
}

// addImports records each file's imports.
func (index *RepositoryIndex) addImports(files []parsing.FileExtraction) {
	for _, file := range files {
		if len(file.Imports) > 0 {
			index.imports[file.Path] = file.Imports
		}
	}
}

// fileFunctions returns the function symbols in a file, ordered by start byte.
func (index *RepositoryIndex) fileFunctions(path string) []Symbol {
	functions := make([]Symbol, 0)
	for _, id := range index.files[path] {
		if index.symbols[id].Kind == parsing.CodeKindFunction {
			functions = append(functions, index.symbols[id])
		}
	}
	return functions
}

// innermostFunction returns the smallest function that contains a call.
func innermostFunction(
	index *RepositoryIndex,
	functions []Symbol,
	call parsing.CallRecord,
) (SymbolID, bool) {
	var best Symbol
	found := false
	for _, function := range functions {
		if call.StartByte < function.StartByte || call.EndByte > function.EndByte {
			continue
		}
		if !found || lessSpan(function, best) {
			best = function
			found = true
		}
	}
	if !found {
		return "", false
	}
	return best.ID, true
}

// dedupeSpellings keeps the first occurrence of each call spelling.
func dedupeSpellings(refs []parsing.CallRef) []parsing.CallRef {
	seen := make(map[string]struct{}, len(refs))
	deduped := make([]parsing.CallRef, 0, len(refs))
	for _, ref := range refs {
		spelling := ref.Spelling()
		if _, exists := seen[spelling]; exists {
			continue
		}
		seen[spelling] = struct{}{}
		deduped = append(deduped, ref)
	}
	return deduped
}

// declarationContains reports whether a type holds a code unit.
func declarationContains(declaration parsing.TypeDeclaration, symbol Symbol) bool {
	return declaration.StartByte <= symbol.StartByte &&
		declaration.EndByte >= symbol.EndByte
}

// containsIdentifier reports whether a name appears as a whole word.
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

// isIdentifierByte reports whether a byte can be part of a name.
func isIdentifierByte(value byte) bool {
	return value == '_' ||
		value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}

// lessSpan orders symbols by the size of their source span, then by position.
func lessSpan(left, right Symbol) bool {
	leftSize := left.EndByte - left.StartByte
	rightSize := right.EndByte - right.StartByte
	if leftSize != rightSize {
		return leftSize < rightSize
	}
	return lessSymbol(left, right)
}

// lessSymbol orders symbols by path, start byte, then name.
func lessSymbol(left, right Symbol) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.StartByte != right.StartByte {
		return left.StartByte < right.StartByte
	}
	return left.Name < right.Name
}

// sortedSymbols returns symbol IDs ordered by their symbol.
func sortedSymbols(index *RepositoryIndex, ids []SymbolID) []SymbolID {
	sorted := append([]SymbolID(nil), ids...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return lessSymbol(index.symbols[sorted[i]], index.symbols[sorted[j]])
	})
	return sorted
}

// sortSymbolsByID orders symbol IDs by their symbol, in place.
func sortSymbolsByID(index *RepositoryIndex, ids []SymbolID) {
	sort.SliceStable(ids, func(i, j int) bool {
		return lessSymbol(index.symbols[ids[i]], index.symbols[ids[j]])
	})
}

// symbolIDs returns the IDs of the given symbols.
func symbolIDs(symbols []Symbol) []SymbolID {
	ids := make([]SymbolID, 0, len(symbols))
	for _, symbol := range symbols {
		ids = append(ids, symbol.ID)
	}
	return ids
}

// appendUnique appends id when it is not already present.
func appendUnique(ids []SymbolID, id SymbolID) []SymbolID {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

// expandCallees keeps the callees that fit the size and count limits. This
// preserves the existing callee budget and skip-over-budget behavior.
func expandCallees(symbols []Symbol) []Symbol {
	if len(symbols) == 0 {
		return nil
	}
	callees := make([]Symbol, 0, len(symbols))
	total := 0
	for _, symbol := range symbols {
		if len(callees) >= maxDirectCallees {
			break
		}
		size := len(symbol.Source)
		if total+size > maxCalleeSourceBytes {
			continue
		}
		callees = append(callees, symbol)
		total += size
	}
	if len(callees) == 0 {
		return nil
	}
	return callees
}
