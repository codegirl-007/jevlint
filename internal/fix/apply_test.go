package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWritesMatchingFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := Apply(root, []FileChange{{
		Path:   "sample.go",
		Before: []byte("before\n"),
		After:  []byte("after\n"),
	}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "after\n" {
		t.Fatalf("Apply() wrote %q, want after", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("Apply() mode = %o, want 640", info.Mode().Perm())
	}
}

func TestApplyRejectsStaleFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Apply(root, []FileChange{{
		Path:   "sample.go",
		Before: []byte("before\n"),
		After:  []byte("after\n"),
	}})
	if err == nil || !strings.Contains(err.Error(), "changed since the proposal") {
		t.Fatalf("Apply() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "changed\n" {
		t.Fatalf("Apply() mutated stale file: %q", content)
	}
}

func TestRestoreChangesWritesBefore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("after\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := RestoreChanges(root, []FileChange{{
		Path:   "sample.go",
		Before: []byte("before\n"),
		After:  []byte("after\n"),
	}})
	if err != nil {
		t.Fatalf("RestoreChanges() error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before\n" {
		t.Fatalf("RestoreChanges() wrote %q, want before", content)
	}
}

func TestApplyRejectsEscapingPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	err := Apply(root, []FileChange{{
		Path:   "../outside.go",
		Before: []byte("before\n"),
		After:  []byte("after\n"),
	}})
	if err == nil || !strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("Apply() error = %v", err)
	}
}
