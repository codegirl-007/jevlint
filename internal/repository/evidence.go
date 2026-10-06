package repository

import (
	"github.com/codegirl-007/jevlint/internal/evidence"
	"github.com/codegirl-007/jevlint/internal/parsing"
)

// EvidenceFor returns the deterministic repository evidence a code unit can
// provide for the requested kinds. Region units (comments, fields, statements)
// resolve to their enclosing declaration.
func (index *RepositoryIndex) EvidenceFor(
	unit parsing.CodeUnit,
	request evidence.Request,
) []evidence.Evidence {
	symbol, ok := index.unitSymbol(unit)
	if !ok {
		return nil
	}
	exact := symbol.Path == unit.Path &&
		symbol.StartByte == unit.StartByte &&
		symbol.Kind == unit.Kind

	items := make([]evidence.Evidence, 0)
	if exact && symbol.Kind == parsing.CodeKindFunction {
		if request.Callees {
			items = append(items, index.calleeEvidence(symbol)...)
		}
		if request.RelatedTypes {
			if containing, ok := index.contained[symbol.ID]; ok {
				items = append(items, symbolEvidence(
					evidence.KindContainingType,
					index.symbols[containing],
					index.displayName(index.symbols[containing]),
				))
			}
		}
	}
	if exact && request.Callers {
		items = append(items, index.callerEvidence(symbol)...)
	}
	if request.RelatedTypes {
		items = append(items, index.relatedEvidence(symbol)...)
	}
	if exact && request.Imports {
		items = append(items, index.importEvidence(symbol)...)
	}
	return items
}

// unitSymbol returns the exact symbol for a unit, or the smallest declaration
// that contains it.
func (index *RepositoryIndex) unitSymbol(unit parsing.CodeUnit) (Symbol, bool) {
	for _, id := range index.files[unit.Path] {
		candidate := index.symbols[id]
		if candidate.Kind == unit.Kind && candidate.StartByte == unit.StartByte {
			return candidate, true
		}
	}
	var best Symbol
	found := false
	for _, id := range index.files[unit.Path] {
		candidate := index.symbols[id]
		if candidate.StartByte > unit.StartByte || candidate.EndByte < unit.EndByte {
			continue
		}
		if candidate.StartByte == unit.StartByte && candidate.EndByte == unit.EndByte {
			continue
		}
		if !found || lessSpan(candidate, best) {
			best = candidate
			found = true
		}
	}
	return best, found
}

// calleeEvidence describes the functions a function directly calls.
func (index *RepositoryIndex) calleeEvidence(caller Symbol) []evidence.Evidence {
	targets := index.calls[caller.ID]
	if len(targets) == 0 {
		return nil
	}
	symbols := make([]Symbol, 0, len(targets))
	for _, id := range targets {
		symbols = append(symbols, index.symbols[id])
	}
	symbols = expandCallees(symbols)
	items := make([]evidence.Evidence, 0, len(symbols))
	for _, target := range symbols {
		items = append(items, symbolEvidence(
			evidence.KindCallee,
			target,
			index.displayName(target),
		))
	}
	return items
}

// callerEvidence describes the functions that directly call a function.
func (index *RepositoryIndex) callerEvidence(callee Symbol) []evidence.Evidence {
	ids := index.callers[callee.ID]
	if len(ids) == 0 {
		return nil
	}
	items := make([]evidence.Evidence, 0, len(ids))
	for _, id := range ids {
		caller := index.symbols[id]
		items = append(items, symbolEvidence(
			evidence.KindCaller,
			caller,
			index.displayName(caller),
		))
	}
	return items
}

// relatedEvidence describes the type declarations a function mentions.
func (index *RepositoryIndex) relatedEvidence(symbol Symbol) []evidence.Evidence {
	declarations := index.related[symbol.ID]
	if len(declarations) == 0 {
		return nil
	}
	items := make([]evidence.Evidence, 0, len(declarations))
	for _, declaration := range declarations {
		items = append(items, evidence.Evidence{
			Kind:      evidence.KindRelatedType,
			Path:      symbol.Path,
			StartLine: declaration.StartLine,
			EndLine:   declaration.EndLine,
			StartByte: declaration.StartByte,
			EndByte:   declaration.EndByte,
			Symbol:    declaration.Name,
			Source:    declaration.Source,
		})
	}
	return items
}

// importEvidence describes the imports a function references.
func (index *RepositoryIndex) importEvidence(symbol Symbol) []evidence.Evidence {
	imports := index.imports[symbol.Path]
	if len(imports) == 0 {
		return nil
	}
	refs := index.callRefs[symbol.ID]
	if len(refs) == 0 {
		return nil
	}
	items := make([]evidence.Evidence, 0)
	seen := make(map[uint]struct{})
	for _, imported := range imports {
		if !index.importReferenced(imported, refs, symbol.Path) {
			continue
		}
		if _, exists := seen[imported.StartByte]; exists {
			continue
		}
		seen[imported.StartByte] = struct{}{}
		items = append(items, evidence.Evidence{
			Kind:      evidence.KindImport,
			Path:      symbol.Path,
			StartLine: imported.StartLine,
			EndLine:   imported.EndLine,
			StartByte: imported.StartByte,
			EndByte:   imported.EndByte,
			Symbol:    imported.Path,
			Source:    imported.Source,
		})
	}
	return items
}

// importReferenced reports whether a call references an import's local name.
func (index *RepositoryIndex) importReferenced(
	imported parsing.Import,
	refs []parsing.CallRef,
	file string,
) bool {
	if imported.Alias == "" {
		return false
	}
	for _, ref := range refs {
		if ref.Path != "" {
			if firstSegment(ref.Path) == imported.Alias {
				return true
			}
			continue
		}
		if ref.Name != imported.Alias {
			continue
		}
		// A local function with the same name shadows the import.
		shadowed := false
		for _, id := range index.byName[ref.Name] {
			if index.symbols[id].Path == file {
				shadowed = true
				break
			}
		}
		if !shadowed {
			return true
		}
	}
	return false
}

// displayName qualifies a method with its containing type.
func (index *RepositoryIndex) displayName(symbol Symbol) string {
	if containing, ok := index.contained[symbol.ID]; ok {
		return index.symbols[containing].Name + "." + symbol.Name
	}
	return symbol.Name
}

// symbolEvidence builds evidence pointing at a symbol declaration.
func symbolEvidence(
	kind evidence.Kind,
	symbol Symbol,
	name string,
) evidence.Evidence {
	return evidence.Evidence{
		Kind:      kind,
		Path:      symbol.Path,
		StartLine: symbol.StartLine,
		EndLine:   symbol.EndLine,
		StartByte: symbol.StartByte,
		EndByte:   symbol.EndByte,
		Symbol:    name,
		Source:    symbol.Source,
	}
}

// firstSegment returns the text before the first dot.
func firstSegment(path string) string {
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			return path[:i]
		}
	}
	return path
}
