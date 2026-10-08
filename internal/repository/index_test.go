package repository

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/evidence"
	"github.com/codegirl-007/jevlint/internal/parsing"
)

type sourceFile struct {
	path string
	body string
}

func buildIndex(t *testing.T, files []sourceFile) (*RepositoryIndex, []parsing.FileExtraction) {
	t.Helper()

	extractor, err := parsing.NewExtractor(map[string]config.Language{
		"go":         {},
		"javascript": {},
		"typescript": {},
		"python":     {},
	})
	if err != nil {
		t.Fatalf("NewExtractor() error = %v", err)
	}

	extractions := make([]parsing.FileExtraction, 0, len(files))
	for _, file := range files {
		extracted, err := extractor.ExtractFile(file.path, []byte(file.body))
		if err != nil {
			t.Fatalf("ExtractFile(%s) error = %v", file.path, err)
		}
		extractions = append(extractions, extracted)
	}
	return New(extractions, "example.com/app"), extractions
}

func functionUnit(t *testing.T, files []parsing.FileExtraction, name string) parsing.CodeUnit {
	t.Helper()
	for _, file := range files {
		for _, unit := range file.Units {
			if unit.Kind == parsing.CodeKindFunction && unit.Name == name {
				return unit
			}
		}
	}
	t.Fatalf("missing function %q", name)
	return parsing.CodeUnit{}
}

func evidenceNames(items []evidence.Evidence, kind evidence.Kind) []string {
	names := make([]string, 0)
	for _, item := range items {
		if item.Kind == kind {
			names = append(names, item.Symbol)
		}
	}
	return names
}

func TestCalleeResolutionMatchesPreviousBehavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files []sourceFile
		want  map[string][]string
	}{
		{
			name:  "same-file unique",
			files: []sourceFile{{"a.go", "package s\nfunc a() { b() }\nfunc b() {}\n"}},
			want:  map[string][]string{"a": {"b"}},
		},
		{
			name:  "source order preserved",
			files: []sourceFile{{"a.go", "package s\nfunc a() { c(); b() }\nfunc b() {}\nfunc c() {}\n"}},
			want:  map[string][]string{"a": {"c", "b"}},
		},
		{
			name:  "duplicate calls collapse",
			files: []sourceFile{{"a.go", "package s\nfunc a() { b(); b() }\nfunc b() {}\n"}},
			want:  map[string][]string{"a": {"b"}},
		},
		{
			name:  "qualified call stays unresolved",
			files: []sourceFile{{"a.go", "package s\nfunc a() { store.Users() }\nfunc Users() {}\n"}},
			want:  map[string][]string{"a": {}},
		},
		{
			name: "project-unique unqualified resolves",
			files: []sourceFile{
				{"a.go", "package s\nfunc a() { load() }\n"},
				{"b.go", "package s\nfunc load() {}\n"},
			},
			want: map[string][]string{"a": {"load"}},
		},
		{
			name: "ambiguous same name stays unresolved",
			files: []sourceFile{
				{"a.go", "package s\nfunc a() { helper() }\n"},
				{"b.go", "package s\nfunc helper() {}\n"},
				{"c.go", "package s\nfunc helper() {}\n"},
			},
			want: map[string][]string{"a": {}},
		},
		{
			name: "nested functions do not leak calls",
			files: []sourceFile{{"a.go",
				"package s\nfunc outer() { inner(); outerCall() }\n" +
					"func inner() { secret() }\nfunc secret() {}\nfunc outerCall() {}\n"}},
			want: map[string][]string{
				"outer":     {"inner", "outerCall"},
				"inner":     {"secret"},
				"outerCall": {},
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			index, files := buildIndex(t, test.files)
			for name, want := range test.want {
				unit := functionUnit(t, files, name)
				got := evidenceNames(
					index.EvidenceFor(unit, evidence.Request{Callees: true}),
					evidence.KindCallee,
				)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s callees = %#v, want %#v", name, got, want)
				}
			}
		})
	}
}

func TestCallers(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\nfunc a() { b() }\nfunc c() { b() }\nfunc b() {}\n"},
	})

	b := functionUnit(t, files, "b")
	got := evidenceNames(
		index.EvidenceFor(b, evidence.Request{Callers: true}),
		evidence.KindCaller,
	)
	if !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("callers = %#v, want [a c]", got)
	}
}

func TestContainingType(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\ntype User struct{}\nfunc (u User) Save() {}\n"},
	})

	save := functionUnit(t, files, "Save")
	got := evidenceNames(
		index.EvidenceFor(save, evidence.Request{RelatedTypes: true}),
		evidence.KindContainingType,
	)
	if !reflect.DeepEqual(got, []string{"User"}) {
		t.Fatalf("containing type = %#v, want [User]", got)
	}
	callees := evidenceNames(
		index.EvidenceFor(save, evidence.Request{Callees: true}),
		evidence.KindCallee,
	)
	_ = callees
}

func TestGoImportEvidence(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go",
			"package s\nimport \"database/sql\"\nfunc a() { sql.Open(\"x\", \"y\") }\n"},
	})

	a := functionUnit(t, files, "a")
	items := index.EvidenceFor(a, evidence.Request{Imports: true})
	if got := evidenceNames(items, evidence.KindImport); !reflect.DeepEqual(got, []string{"database/sql"}) {
		t.Fatalf("imports = %#v, want [database/sql]", got)
	}
}

func TestScriptImportEvidence(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.ts", "import * as api from \"./client\";\nexport function a() { api.get() }\n"},
	})

	a := functionUnit(t, files, "a")
	if got := evidenceNames(
		index.EvidenceFor(a, evidence.Request{Imports: true}),
		evidence.KindImport,
	); !reflect.DeepEqual(got, []string{"./client"}) {
		t.Fatalf("imports = %#v, want [./client]", got)
	}
}

func TestQualifiedCallResolvesWithImportEvidence(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"internal/auth/auth.go", "package auth\nfunc Validate() {}\n"},
		{"cmd/main.go", "package main\nimport \"example.com/app/internal/auth\"\nfunc run() { auth.Validate() }\n"},
	})

	run := functionUnit(t, files, "run")
	got := evidenceNames(
		index.EvidenceFor(run, evidence.Request{Callees: true}),
		evidence.KindCallee,
	)
	if !reflect.DeepEqual(got, []string{"Validate"}) {
		t.Fatalf("callees = %#v, want [Validate]", got)
	}
}

func TestQualifiedCallWithoutImportStaysUnresolved(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\nfunc a() { store.Users() }\n"},
		{"b.go", "package s\nfunc Users() {}\n"},
	})

	a := functionUnit(t, files, "a")
	if got := evidenceNames(
		index.EvidenceFor(a, evidence.Request{Callees: true}),
		evidence.KindCallee,
	); len(got) != 0 {
		t.Fatalf("callees = %#v, want none", got)
	}
}

func TestAmbiguousImportedQualifiedCallStaysUnresolved(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"internal/auth/one.go", "package auth\nfunc Validate() {}\n"},
		{"internal/auth/two.go", "package auth\nfunc Validate() {}\n"},
		{"cmd/main.go", "package main\nimport \"example.com/app/internal/auth\"\nfunc run() { auth.Validate() }\n"},
	})

	run := functionUnit(t, files, "run")
	if got := evidenceNames(
		index.EvidenceFor(run, evidence.Request{Callees: true}),
		evidence.KindCallee,
	); len(got) != 0 {
		t.Fatalf("callees = %#v, want none", got)
	}
}

func TestExternalModuleImportStaysUnresolved(t *testing.T) {
	t.Parallel()

	// The external path ends with the local directory "internal/auth", but it is
	// not inside this repository's module, so it must not bind to local code.
	index, files := buildIndex(t, []sourceFile{
		{"internal/auth/auth.go", "package auth\nfunc Validate() {}\n"},
		{"cmd/main.go", "package main\nimport \"github.com/other/project/internal/auth\"\nfunc run() { auth.Validate() }\n"},
	})
	run := functionUnit(t, files, "run")
	if got := evidenceNames(
		index.EvidenceFor(run, evidence.Request{Callees: true}),
		evidence.KindCallee,
	); len(got) != 0 {
		t.Fatalf("external import resolved to local code: %#v", got)
	}
}

func TestDocumentedGoMethodKeepsContainingType(t *testing.T) {
	t.Parallel()

	for _, receiver := range []string{"User", "*User"} {
		index, files := buildIndex(t, []sourceFile{
			{"user.go",
				"package s\ntype User struct{}\n\n// Save persists the user.\nfunc (u " + receiver + ") Save() {}\n"},
		})
		save := functionUnit(t, files, "Save")
		if got := evidenceNames(
			index.EvidenceFor(save, evidence.Request{RelatedTypes: true}),
			evidence.KindContainingType,
		); !reflect.DeepEqual(got, []string{"User"}) {
			t.Fatalf("receiver %q: containing type = %#v, want [User]", receiver, got)
		}
	}
}

func TestImportEvidenceFromTypePosition(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\nimport \"context\"\nfunc Handle(ctx context.Context) {}\n"},
	})
	handle := functionUnit(t, files, "Handle")
	if got := evidenceNames(
		index.EvidenceFor(handle, evidence.Request{Imports: true}),
		evidence.KindImport,
	); !reflect.DeepEqual(got, []string{"context"}) {
		t.Fatalf("imports = %#v, want [context]", got)
	}
}

func TestRelatedTypeEvidence(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\ntype User struct{}\nfunc save(u User) {}\n"},
	})

	save := functionUnit(t, files, "save")
	got := evidenceNames(
		index.EvidenceFor(save, evidence.Request{RelatedTypes: true}),
		evidence.KindRelatedType,
	)
	if !reflect.DeepEqual(got, []string{"User"}) {
		t.Fatalf("related types = %#v, want [User]", got)
	}
}

func TestCalleeBudgetIsPreserved(t *testing.T) {
	t.Parallel()

	var source strings.Builder
	source.WriteString("package s\nfunc caller() {\n")
	for index := 0; index < maxDirectCallees+3; index++ {
		fmt.Fprintf(&source, "\tf%d()\n", index)
	}
	source.WriteString("}\n")
	for index := 0; index < maxDirectCallees+3; index++ {
		fmt.Fprintf(&source, "func f%d() {}\n", index)
	}

	index, files := buildIndex(t, []sourceFile{{"a.go", source.String()}})
	caller := functionUnit(t, files, "caller")
	got := evidenceNames(
		index.EvidenceFor(caller, evidence.Request{Callees: true}),
		evidence.KindCallee,
	)
	if len(got) != maxDirectCallees {
		t.Fatalf("callees = %d, want %d", len(got), maxDirectCallees)
	}
	if got[0] != "f0" || got[len(got)-1] != "f11" {
		t.Fatalf("callees = %#v, want f0..f11 in order", got)
	}
}

func TestEvidenceCarriesProvenance(t *testing.T) {
	t.Parallel()

	index, files := buildIndex(t, []sourceFile{
		{"a.go", "package s\n\nfunc caller() {\n\tcallee()\n}\n\nfunc callee() {}\n"},
	})

	caller := functionUnit(t, files, "caller")
	items := index.EvidenceFor(caller, evidence.Request{Callees: true})
	if len(items) != 1 {
		t.Fatalf("evidence = %#v, want one item", items)
	}
	item := items[0]
	if item.Path != "a.go" || item.StartLine != 7 || item.Symbol != "callee" {
		t.Fatalf("evidence provenance = %#v", item)
	}
	if !strings.Contains(item.Source, "func callee") {
		t.Fatalf("evidence source = %q", item.Source)
	}
}

func TestEvidenceOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	files := []sourceFile{
		{"a.go", "package s\nfunc a() { b(); c() }\nfunc b() {}\nfunc c() {}\n"},
	}
	first, firstFiles := buildIndex(t, files)
	second, secondFiles := buildIndex(t, files)

	unitA := functionUnit(t, firstFiles, "a")
	unitB := functionUnit(t, secondFiles, "a")
	left := first.EvidenceFor(unitA, evidence.Request{Callees: true, Callers: true})
	right := second.EvidenceFor(unitB, evidence.Request{Callees: true, Callers: true})
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("evidence order differs:\n%#v\n%#v", left, right)
	}
}
