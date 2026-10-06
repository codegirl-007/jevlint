package parsing

import (
	"reflect"
	"testing"
)

func importsOf(t *testing.T, path string, source string) []Import {
	t.Helper()

	file, err := testExtractorForPath(t, path).ExtractFile(path, []byte(source))
	if err != nil {
		t.Fatalf("ExtractFile(%s) error = %v", path, err)
	}
	return file.Imports
}

func importPairs(imports []Import) [][2]string {
	pairs := make([][2]string, 0, len(imports))
	for _, imported := range imports {
		pairs = append(pairs, [2]string{imported.Path, imported.Alias})
	}
	return pairs
}

func TestExtractGoImports(t *testing.T) {
	t.Parallel()

	imports := importsOf(t, "sample.go", `package sample

import (
	"database/sql"
	alias "example.com/x"
	_ "example.com/blank"
	. "example.com/dot"
	"github.com/foo/bar/v2"
)
`)
	want := [][2]string{
		{"database/sql", "sql"},
		{"example.com/x", "alias"},
		{"example.com/blank", ""},
		{"example.com/dot", ""},
		{"github.com/foo/bar/v2", "bar"},
	}
	if got := importPairs(imports); !reflect.DeepEqual(got, want) {
		t.Fatalf("go imports = %#v, want %#v", got, want)
	}
}

func TestExtractScriptImports(t *testing.T) {
	t.Parallel()

	imports := importsOf(t, "sample.ts", `import def from "a";
import * as ns from "b";
import { x, y as z } from "c";
import "side";
`)
	want := [][2]string{
		{"a", "def"},
		{"b", "ns"},
		{"c", "x"},
		{"c", "z"},
		{"side", ""},
	}
	if got := importPairs(imports); !reflect.DeepEqual(got, want) {
		t.Fatalf("script imports = %#v, want %#v", got, want)
	}
}

func TestExtractPythonImports(t *testing.T) {
	t.Parallel()

	imports := importsOf(t, "sample.py", `import a.b.c
import d as e
from f.g import h, i as j
`)
	want := [][2]string{
		{"a.b.c", "a"},
		{"d", "e"},
		{"f.g", "h"},
		{"f.g", "j"},
	}
	if got := importPairs(imports); !reflect.DeepEqual(got, want) {
		t.Fatalf("python imports = %#v, want %#v", got, want)
	}
}
