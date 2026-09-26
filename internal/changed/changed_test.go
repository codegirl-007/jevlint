package changed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesReportsStagedUnstagedAndUntracked(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "tracked.go", "package sample\n")
	git(t, root, "add", "tracked.go")
	git(t, root, "commit", "-m", "initial")

	writeFile(t, root, "tracked.go", "package sample\n\nfunc Unstaged() {}\n")
	writeFile(t, root, "staged.go", "package sample\n")
	git(t, root, "add", "staged.go")
	writeFile(t, root, "untracked.go", "package sample\n")

	files, err := Files(root)
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	assertFiles(t, files, "staged.go", "tracked.go", "untracked.go")
}

func TestFilesOmitsDeletesAndKeepsRenameTarget(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "keep.go", "package keep\n")
	writeFile(t, root, "gone.go", "package gone\n")
	writeFile(t, root, "old.go", "package old\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")

	git(t, root, "rm", "gone.go")
	git(t, root, "mv", "old.go", "new.go")

	files, err := Files(root)
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	assertFiles(t, files, "new.go")
}

func TestFilesDropsPathsOutsideProjectRoot(t *testing.T) {
	repo := initRepo(t)
	project := filepath.Join(repo, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	writeFile(t, repo, "outside.go", "package outside\n")
	writeFile(t, project, "inside.go", "package inside\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "outside.go", "package outside\n\nfunc Changed() {}\n")
	writeFile(t, project, "inside.go", "package inside\n\nfunc Changed() {}\n")

	files, err := Files(project)
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	assertFiles(t, files, "inside.go")
}

func TestFilesRequiresGitRepository(t *testing.T) {
	requireGit(t)
	_, err := Files(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "--changed requires a git repository") {
		t.Fatalf("Files() error = %v", err)
	}
}

func TestIntersectKeepsRequestedSubtree(t *testing.T) {
	t.Parallel()

	files := []string{"src/a.go", "src/nested/b.go", "other.go"}
	got := Intersect(files, []string{"src"})
	assertFiles(t, got, "src/a.go", "src/nested/b.go")
}

func TestRelativizeDropsPathsOutsideRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := Relativize(root, []string{
		"src/a.go",
		filepath.Join(root, "inside.go"),
		filepath.Join(filepath.Dir(root), "outside.go"),
	})
	if err != nil {
		t.Fatalf("Relativize() error = %v", err)
	}
	assertFiles(t, got, "src/a.go", "inside.go")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for --changed tests")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)

	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.email", "jevlint@example.com")
	git(t, root, "config", "user.name", "jevlint")
	git(t, root, "config", "commit.gpgsign", "false")
	return root
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=jevlint",
		"GIT_AUTHOR_EMAIL=jevlint@example.com",
		"GIT_COMMITTER_NAME=jevlint",
		"GIT_COMMITTER_EMAIL=jevlint@example.com",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func writeFile(t *testing.T, root string, relative string, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

func assertFiles(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
	for index, path := range want {
		if got[index] != path {
			t.Fatalf("files = %#v, want %#v", got, want)
		}
	}
}
