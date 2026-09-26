package changed

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Files lists the working-tree paths --changed should evaluate. Git's
// delete records are skipped because there is no source left to lint;
// renames keep only the destination. Anything outside projectRoot is
// ignored so a monorepo checkout cannot pull sibling packages into this
// project's check.
func Files(projectRoot string) ([]string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	gitRoot, porcelain, err := gitDirtyListing(root)
	if err != nil {
		return nil, err
	}
	return existingProjectFiles(root, gitRoot, parsePorcelain(porcelain))
}

func gitDirtyListing(root string) ([]byte, []byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil, fmt.Errorf("git is required for --changed")
	}
	gitRoot, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, wrapGitError(err)
	}
	porcelain, err := gitOutput(root, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, nil, wrapGitError(err)
	}
	return gitRoot, porcelain, nil
}

func existingProjectFiles(root string, gitRoot []byte, gitPaths []string) ([]string, error) {
	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, gitPath := range gitPaths {
		absolute := filepath.Join(string(gitRoot), filepath.FromSlash(gitPath))
		relative, ok := underRoot(root, absolute)
		if !ok {
			continue
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, fmt.Errorf("inspect dirty path %q: %w", relative, statErr)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if _, exists := seen[relative]; exists {
			continue
		}
		seen[relative] = struct{}{}
		files = append(files, relative)
	}
	sort.Strings(files)
	return files, nil
}

func Relativize(projectRoot string, requested []string) ([]string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	paths := make([]string, 0, len(requested))
	for _, requestedPath := range requested {
		absolute := requestedPath
		if !filepath.IsAbs(requestedPath) {
			absolute = filepath.Join(root, requestedPath)
		}
		relative, ok := underRoot(root, filepath.Clean(absolute))
		if !ok {
			continue
		}
		paths = append(paths, relative)
	}
	return paths, nil
}

func Intersect(files []string, requested []string) []string {
	if len(requested) == 0 {
		return files
	}
	matched := make([]string, 0)
	for _, file := range files {
		if matchesRequested(file, requested) {
			matched = append(matched, file)
		}
	}
	return matched
}

func matchesRequested(file string, requested []string) bool {
	cleaned := filepath.Clean(file)
	for _, request := range requested {
		request = filepath.Clean(request)
		if cleaned == request {
			return true
		}
		prefix := request + string(filepath.Separator)
		if strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	return false
}

func underRoot(root string, absolute string) (string, bool) {
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return "", false
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

const (
	porcelainNUL            = 0
	porcelainStatusWidth    = 2
	porcelainPathOffset     = 3
	porcelainRenameStatus = 'R'
	porcelainCopyStatus   = 'C'
)

func parsePorcelain(data []byte) []string {
	fields := bytes.Split(data, []byte{porcelainNUL})
	paths := make([]string, 0)
	for index := 0; index < len(fields); {
		field := fields[index]
		index++
		if len(field) < porcelainPathOffset {
			continue
		}
		// -z rename/copy records are: "XY newpath\0oldpath\0"
		path := string(field[porcelainPathOffset:])
		if isRenameOrCopy(field[:porcelainStatusWidth]) && index < len(fields) {
			index++
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func isRenameOrCopy(xy []byte) bool {
	return xy[0] == porcelainRenameStatus ||
		xy[0] == porcelainCopyStatus ||
		xy[1] == porcelainRenameStatus ||
		xy[1] == porcelainCopyStatus
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message == "" {
				message = strings.TrimSpace(string(output))
			}
			if strings.Contains(message, "not a git repository") {
				return nil, fmt.Errorf("--changed requires a git repository")
			}
			return nil, fmt.Errorf("git %s: %s", args[0], message)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	if args[0] == "rev-parse" {
		return bytes.TrimSpace(output), nil
	}
	return output, nil
}

func wrapGitError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("git is required for --changed")
	}
	return err
}

