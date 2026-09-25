package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jevlint/internal/runner"
)

func TestMirrorProjectBuildsSafeProjectSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspaceFile(t, root, ".gitignore", "*.tmp\n!keep.tmp\n", 0o600)
	writeWorkspaceFile(
		t,
		root,
		"nested/.gitignore",
		"*.generated\n!important.generated\n",
		0o600,
	)
	for path, content := range map[string]string{
		"sample.go":                  "package sample\n",
		"sample_test.go":             "package sample\n",
		"go.mod":                     "module sample\n",
		"README.md":                  "# Sample\n",
		"drop.tmp":                   "ignored\n",
		"keep.tmp":                   "kept\n",
		"nested/drop.generated":      "ignored\n",
		"nested/important.generated": "kept\n",
		"node_modules/dependency.js": "ignored\n",
		".env":                       "TOKEN=secret\n",
	} {
		writeWorkspaceFile(t, root, path, content, 0o600)
	}
	writeWorkspaceFile(t, root, "scripts/check", "#!/bin/sh\n", 0o700)
	if err := os.Symlink(
		filepath.Join(root, "sample.go"),
		filepath.Join(root, "linked.go"),
	); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	before, err := mirrorProject(root, workspace, Options{})
	if err != nil {
		t.Fatalf("mirrorProject() error = %v", err)
	}
	for _, expected := range []string{
		".gitignore",
		"README.md",
		"go.mod",
		"keep.tmp",
		"nested/.gitignore",
		"nested/important.generated",
		"sample.go",
		"sample_test.go",
		"scripts/check",
	} {
		if _, ok := before[expected]; !ok {
			t.Errorf("snapshot is missing %q: %#v", expected, before)
		}
	}
	for _, excluded := range []string{
		".env",
		"drop.tmp",
		"linked.go",
		"nested/drop.generated",
		"node_modules/dependency.js",
	} {
		if _, ok := before[excluded]; ok {
			t.Errorf("snapshot contains excluded path %q", excluded)
		}
	}
	info, err := os.Stat(filepath.Join(workspace, "scripts", "check"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mirrored script mode = %o, want 700", info.Mode().Perm())
	}
}

func TestMirrorProjectAppliesContextAndExcludePatterns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for path, content := range map[string]string{
		"sample.go":            "package sample\n",
		"sibling.go":           "package sample\n",
		"docs/public.md":       "public\n",
		"docs/private/note.md": "private\n",
		"jevlint.json":         "{}\n",
	} {
		writeWorkspaceFile(t, root, path, content, 0o600)
	}

	before, err := mirrorProject(root, t.TempDir(), Options{
		ConfigPath: filepath.Join(root, "jevlint.json"),
		Context:    []string{"docs/**"},
		Exclude:    []string{"docs/private/**"},
		Findings:   []runner.Finding{testFinding()},
	})
	if err != nil {
		t.Fatalf("mirrorProject() error = %v", err)
	}
	for _, expected := range []string{"docs/public.md", "jevlint.json", "sample.go"} {
		if _, ok := before[expected]; !ok {
			t.Errorf("snapshot is missing required path %q: %#v", expected, before)
		}
	}
	for _, excluded := range []string{"docs/private/note.md", "sibling.go"} {
		if _, ok := before[excluded]; ok {
			t.Errorf("snapshot contains excluded path %q", excluded)
		}
	}
}

func TestMirrorProjectRejectsRequiredSecretFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspaceFile(t, root, ".env", "TOKEN=secret\n", 0o600)
	finding := testFinding()
	finding.Path = ".env"

	_, err := mirrorProject(root, t.TempDir(), Options{
		Findings: []runner.Finding{finding},
	})
	if err == nil || !strings.Contains(err.Error(), "excluded by the safety policy") {
		t.Fatalf("mirrorProject() error = %v", err)
	}
}

func writeWorkspaceFile(
	t *testing.T,
	root string,
	path string,
	content string,
	mode os.FileMode,
) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
