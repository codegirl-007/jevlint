package parsing

import (
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// CallRef is a called name as it appears in the source.
type CallRef struct {
	Name string
	Path string
}

// Spelling returns the called name with its qualifier.
func (ref CallRef) Spelling() string {
	if ref.Path == "" {
		return ref.Name
	}
	return ref.Path + "." + ref.Name
}

// extractCalls records every syntactic call in a file, in source order.
func extractCalls(spec languageSpec, source []byte, root *tree_sitter.Node) []CallRecord {
	if spec.callQuery == nil {
		return nil
	}

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()

	callIndex, ok := namedCaptureIndex(spec.callQuery, "call")
	if !ok {
		return nil
	}

	calls := make([]CallRecord, 0)
	matches := cursor.Matches(spec.callQuery, root, source)
	for {
		match := matches.Next()
		if match == nil {
			break
		}
		for index := range match.Captures {
			capture := &match.Captures[index]
			if capture.Index != callIndex {
				continue
			}
			ref := parseCallSpelling(capture.Node.Utf8Text(source))
			if ref.Name == "" {
				continue
			}
			node := capture.Node
			calls = append(calls, CallRecord{
				Ref:       ref,
				StartLine: node.StartPosition().Row + 1,
				EndLine:   node.EndPosition().Row + 1,
				StartByte: node.StartByte(),
				EndByte:   node.EndByte(),
				Source:    node.Utf8Text(source),
			})
		}
	}
	sort.SliceStable(calls, func(i, j int) bool {
		if calls[i].StartByte != calls[j].StartByte {
			return calls[i].StartByte < calls[j].StartByte
		}
		return calls[i].EndByte < calls[j].EndByte
	})
	return calls
}

// parseCallSpelling splits a called name into its qualifier and name.
func parseCallSpelling(text string) CallRef {
	spelling := strings.TrimSpace(text)
	if spelling == "" {
		return CallRef{}
	}
	dot := strings.LastIndex(spelling, ".")
	if dot <= 0 || dot == len(spelling)-1 {
		return CallRef{Name: spelling}
	}
	return CallRef{
		Path: spelling[:dot],
		Name: spelling[dot+1:],
	}
}

// namedCaptureIndex returns the position of a named capture in a query.
func namedCaptureIndex(query *tree_sitter.Query, name string) (uint32, bool) {
	for index, capture := range query.CaptureNames() {
		if capture == name {
			return uint32(index), true
		}
	}
	return 0, false
}
