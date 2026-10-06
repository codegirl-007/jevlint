// Package evidence describes deterministic repository facts gathered by
// Jevlint and the requests that ask for them. Every item carries provenance:
// the path, span, symbol, and source it was read from.
package evidence

import "sort"

// Kind names the relationship an evidence item describes.
type Kind string

const (
	KindCallee         Kind = "callee"
	KindCaller         Kind = "caller"
	KindContainingType Kind = "containing-type"
	KindRelatedType    Kind = "related-type"
	KindImport         Kind = "import"
)

// Evidence is one verifiable repository fact attached to a code unit.
type Evidence struct {
	Kind      Kind   `json:"kind"`
	Path      string `json:"path"`
	StartLine uint   `json:"startLine"`
	EndLine   uint   `json:"endLine"`
	StartByte uint   `json:"startByte,omitempty"`
	EndByte   uint   `json:"endByte,omitempty"`
	Symbol    string `json:"symbol"`
	Source    string `json:"source"`
}

// Request names the evidence kinds a rule asks for.
type Request struct {
	Callees      bool
	Callers      bool
	RelatedTypes bool
	Imports      bool
}

// Empty reports whether the request asks for nothing.
func (request Request) Empty() bool {
	return !request.Callees &&
		!request.Callers &&
		!request.RelatedTypes &&
		!request.Imports
}

// kindOrder is the fixed order evidence kinds appear in a result. Callees keep
// their resolution order; this only orders the groups relative to each other.
var kindOrder = map[Kind]int{
	KindCallee:         0,
	KindCaller:         1,
	KindContainingType: 2,
	KindRelatedType:    3,
	KindImport:         4,
}

// SortOrdersAndProvenance orders evidence by kind group and then by concrete
// provenance, so equal inputs always produce equal output. It never reorders
// callee evidence within its group; callers of this function should sort a
// single kind at a time when that order matters.
func Sort(items []Evidence) {
	sort.SliceStable(items, func(i, j int) bool {
		return less(items[i], items[j])
	})
}

func less(left, right Evidence) bool {
	if kindOrder[left.Kind] != kindOrder[right.Kind] {
		return kindOrder[left.Kind] < kindOrder[right.Kind]
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.StartByte != right.StartByte {
		return left.StartByte < right.StartByte
	}
	if left.Symbol != right.Symbol {
		return left.Symbol < right.Symbol
	}
	return left.Source < right.Source
}
