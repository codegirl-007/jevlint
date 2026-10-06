package parsing

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

const (
	maxDirectCallees     = 12
	maxCalleeSourceBytes = 16 << 10
)

// CallRef is a called name as it appears in the source.
type CallRef struct {
	Name string
	Path string
}

// CalleeContext is the source of a called function sent as context.
type CalleeContext struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Source    string `json:"source"`
	StartLine uint   `json:"startLine"`
	EndLine   uint   `json:"endLine"`
	StartByte uint   `json:"startByte"`
	EndByte   uint   `json:"endByte"`
}

// Spelling returns the called name with its qualifier.
func (ref CallRef) Spelling() string {
	if ref.Path == "" {
		return ref.Name
	}
	return ref.Path + "." + ref.Name
}

// WithCalleeContext copies a unit and adds the functions it calls.
func WithCalleeContext(unit CodeUnit) CodeUnit {
	clone := unit
	clone.Callees = ExpandCallees(unit.Resolved)
	return clone
}

// ExpandCallees keeps the callees that fit the size and count limits.
func ExpandCallees(resolved []CalleeContext) []CalleeContext {
	if len(resolved) == 0 {
		return nil
	}
	callees := make([]CalleeContext, 0, len(resolved))
	total := 0
	for _, callee := range resolved {
		if len(callees) >= maxDirectCallees {
			break
		}
		size := len(callee.Source)
		if total+size > maxCalleeSourceBytes {
			continue
		}
		callees = append(callees, callee)
		total += size
	}
	if len(callees) == 0 {
		return nil
	}
	return callees
}

// ResolveCallees links each function to the functions it calls.
func ResolveCallees(functions []*CodeUnit) {
	index := make(map[string][]*CodeUnit)
	for _, function := range functions {
		if function == nil || function.Kind != CodeKindFunction {
			continue
		}
		index[function.Name] = append(index[function.Name], function)
	}
	for _, function := range functions {
		if function == nil || function.Kind != CodeKindFunction {
			continue
		}
		function.Resolved = resolveFunctionCallees(*function, index)
	}
}

// resolveFunctionCallees finds the functions called by one function.
func resolveFunctionCallees(
	function CodeUnit,
	index map[string][]*CodeUnit,
) []CalleeContext {
	resolved := make([]CalleeContext, 0, len(function.CallRefs))
	seen := make(map[string]struct{}, len(function.CallRefs))
	for _, ref := range function.CallRefs {
		if ref.Name == "" || ref.Name == function.Name {
			continue
		}
		target := resolveCallRef(ref, function, index)
		if target == nil {
			continue
		}
		identity := calleeIdentity(*target)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		resolved = append(resolved, CalleeContext{
			Name:      target.Name,
			Path:      target.Path,
			Source:    target.Source,
			StartLine: target.StartLine,
			EndLine:   target.EndLine,
			StartByte: target.StartByte,
			EndByte:   target.EndByte,
		})
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// resolveCallRef finds the one function a call can mean, or nothing when unsure.
// Qualified calls are not bound: without receiver or package information a
// qualifier cannot be checked against the bare function index, so binding on
// the final name alone would attach external calls to same-named functions.
func resolveCallRef(
	ref CallRef,
	caller CodeUnit,
	index map[string][]*CodeUnit,
) *CodeUnit {
	if ref.Path != "" {
		return nil
	}
	candidates := index[ref.Name]
	if len(candidates) == 0 {
		return nil
	}

	sameFile := make([]*CodeUnit, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Path == caller.Path {
			sameFile = append(sameFile, candidate)
		}
	}
	if len(sameFile) == 1 {
		return sameFile[0]
	}
	if len(sameFile) > 1 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return nil
}

// calleeIdentity returns a value that identifies a callee.
func calleeIdentity(unit CodeUnit) string {
	return strings.Join([]string{
		unit.Path,
		unit.Name,
		unit.Source,
	}, "\x00")
}

// attachCallRefs records the calls made inside each function.
func attachCallRefs(
	spec languageSpec,
	source []byte,
	root *tree_sitter.Node,
	functions []CodeUnit,
) {
	if spec.callQuery == nil || len(functions) == 0 {
		return
	}

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()

	callIndex, ok := namedCaptureIndex(spec.callQuery, "call")
	if !ok {
		return
	}

	type locatedCall struct {
		start uint
		end   uint
		ref   CallRef
	}
	calls := make([]locatedCall, 0)
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
			calls = append(calls, locatedCall{
				start: capture.Node.StartByte(),
				end:   capture.Node.EndByte(),
				ref:   ref,
			})
		}
	}

	for index := range functions {
		if functions[index].Kind != CodeKindFunction {
			continue
		}
		seen := make(map[string]struct{})
		for _, call := range calls {
			if call.start < functions[index].StartByte ||
				call.end > functions[index].EndByte {
				continue
			}
			if enclosedByInnerFunction(call.start, call.end, functions[index], functions) {
				continue
			}
			spelling := call.ref.Spelling()
			if _, exists := seen[spelling]; exists {
				continue
			}
			seen[spelling] = struct{}{}
			functions[index].CallRefs = append(functions[index].CallRefs, call.ref)
		}
	}
}

// enclosedByInnerFunction reports whether a call sits inside a nested function.
func enclosedByInnerFunction(
	start uint,
	end uint,
	outer CodeUnit,
	functions []CodeUnit,
) bool {
	for _, candidate := range functions {
		if candidate.Kind != CodeKindFunction ||
			candidate.StartByte == outer.StartByte && candidate.EndByte == outer.EndByte &&
				candidate.Name == outer.Name && candidate.Path == outer.Path {
			continue
		}
		if candidate.StartByte < outer.StartByte || candidate.EndByte > outer.EndByte {
			continue
		}
		if start >= candidate.StartByte && end <= candidate.EndByte {
			return true
		}
	}
	return false
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
