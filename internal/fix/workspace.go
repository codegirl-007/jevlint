package fix

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	ignore "github.com/sabhiram/go-gitignore"
)

var excludedDirectoryNames = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".venv":        {},
	".cache":       {},
	".next":        {},
	".turbo":       {},
	"build":        {},
	"coverage":     {},
	"dist":         {},
	"node_modules": {},
	"target":       {},
	"vendor":       {},
	"venv":         {},
}

type snapshotFile struct {
	content []byte
	perm    os.FileMode
}

type snapshotPath struct {
	file     snapshotFile
	captured bool
}

type projectSnapshot struct {
	paths   map[string]snapshotPath
	exclude []string
}

func snapshotProject(root string, options Options) (projectSnapshot, error) {
	required, err := requiredProjectPaths(root, options)
	if err != nil {
		return projectSnapshot{}, err
	}
	knownPaths, err := discoverProjectPaths(root, nil, options.Workspace.Paths.Exclude)
	if err != nil {
		return projectSnapshot{}, err
	}
	paths, err := collectSnapshotPaths(root, required, options)
	if err != nil {
		return projectSnapshot{}, err
	}
	snapshot := projectSnapshot{
		paths:   make(map[string]snapshotPath, len(knownPaths)+len(required)),
		exclude: options.Workspace.Paths.Exclude,
	}
	for _, relative := range knownPaths {
		relative = filepath.ToSlash(relative)
		if _, exists := snapshot.paths[relative]; !exists {
			snapshot.paths[relative] = snapshotPath{}
		}
	}
	for _, relative := range paths {
		if err := addSnapshotFile(&snapshot, root, relative, required); err != nil {
			return projectSnapshot{}, err
		}
	}
	return snapshot, nil
}

func collectSnapshotPaths(
	root string,
	required map[string]struct{},
	options Options,
) ([]string, error) {
	paths, err := discoverProjectPaths(root, options.Workspace.Paths.Context, options.Workspace.Paths.Exclude)
	if err != nil {
		return nil, err
	}
	for path := range required {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func addSnapshotFile(
	snapshot *projectSnapshot,
	root string,
	relative string,
	required map[string]struct{},
) error {
	relative = filepath.ToSlash(relative)
	if entry, exists := snapshot.paths[relative]; exists && entry.captured {
		return nil
	}
	if !filepath.IsLocal(filepath.FromSlash(relative)) {
		return fmt.Errorf("project path escapes root: %q", relative)
	}
	if skipped, err := skipExcludedSnapshotFile(relative, required); skipped || err != nil {
		return err
	}
	file, err := readSnapshotFile(root, relative)
	if err != nil {
		return err
	}
	snapshot.paths[relative] = snapshotPath{file: file, captured: true}
	return nil
}

func readSnapshotFile(root string, relative string) (snapshotFile, error) {
	sourcePath := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return snapshotFile{}, fmt.Errorf("inspect %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return snapshotFile{}, fmt.Errorf("project file %q is not a regular file", relative)
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return snapshotFile{}, fmt.Errorf("read %q: %w", relative, err)
	}
	return snapshotFile{
		content: content,
		perm:    info.Mode().Perm(),
	}, nil
}

func skipExcludedSnapshotFile(
	relative string,
	required map[string]struct{},
) (bool, error) {
	if !isHardExcludedFile(relative) {
		return false, nil
	}
	if _, ok := required[relative]; ok {
		return true, fmt.Errorf(
			"required fix file %q is excluded by the safety policy",
			relative,
		)
	}
	return true, nil
}

func removeUnknownProjectFiles(root string, snapshot projectSnapshot) error {
	current, err := discoverProjectPaths(root, nil, snapshot.exclude)
	if err != nil {
		return err
	}
	for _, relative := range current {
		relative = filepath.ToSlash(relative)
		if _, ok := snapshot.paths[relative]; ok {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove unexpected file %q: %w", relative, err)
		}
	}
	return nil
}

func restoreSnapshotFiles(root string, snapshot projectSnapshot) error {
	for relative, entry := range snapshot.paths {
		if !entry.captured {
			continue
		}
		file := entry.file
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("restore directory for %q: %w", relative, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace %q: %w", relative, err)
		}
		if err := os.WriteFile(path, file.content, file.perm); err != nil {
			return fmt.Errorf("restore %q: %w", relative, err)
		}
	}
	return nil
}

func requiredProjectPaths(
	root string,
	options Options,
) (map[string]struct{}, error) {
	required := make(map[string]struct{}, len(options.Findings)+1)
	for _, finding := range options.Findings {
		relative := filepath.ToSlash(filepath.Clean(finding.Path))
		if !filepath.IsLocal(filepath.FromSlash(relative)) {
			return nil, fmt.Errorf("finding path escapes project root: %q", finding.Path)
		}
		required[relative] = struct{}{}
	}
	if options.Workspace.ConfigPath == "" {
		return required, nil
	}
	absoluteConfig, err := filepath.Abs(options.Workspace.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	relativeConfig, err := filepath.Rel(root, absoluteConfig)
	if err != nil || !filepath.IsLocal(relativeConfig) {
		return nil, fmt.Errorf("config path must be inside project root")
	}
	required[filepath.ToSlash(relativeConfig)] = struct{}{}
	return required, nil
}

func discoverProjectPaths(
	root string,
	contextPatterns []string,
	excludePatterns []string,
) ([]string, error) {
	state := projectWalkState{
		context: contextPatterns,
		exclude: excludePatterns,
		matcher: ignore.CompileIgnoreLines(),
		paths:   make([]string, 0),
	}
	err := filepath.WalkDir(root, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		return state.collectEntry(root, path, entry, walkErr)
	})
	if err != nil {
		return nil, fmt.Errorf("discover fix context: %w", err)
	}
	return state.paths, nil
}

type projectWalkState struct {
	context  []string
	exclude  []string
	patterns []string
	matcher  *ignore.GitIgnore
	paths    []string
}

func (state *projectWalkState) collectEntry(
	root string,
	path string,
	entry fs.DirEntry,
	walkErr error,
) error {
	if walkErr != nil {
		return walkErr
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if relative == "." {
		nested, readErr := readIgnorePatterns(path, "")
		state.patterns = append(state.patterns, nested...)
		state.matcher = ignore.CompileIgnoreLines(state.patterns...)
		return readErr
	}
	relative = filepath.ToSlash(relative)
	if entry.IsDir() {
		return state.collectDirectory(path, entry.Name(), relative)
	}
	return state.collectFile(entry, relative)
}

func (state *projectWalkState) collectDirectory(
	path string,
	name string,
	relative string,
) error {
	if _, excluded := excludedDirectoryNames[name]; excluded || state.matcher.MatchesPath(relative+"/") {
		return filepath.SkipDir
	}
	nested, readErr := readIgnorePatterns(path, relative)
	state.patterns = append(state.patterns, nested...)
	state.matcher = ignore.CompileIgnoreLines(state.patterns...)
	return readErr
}

func (state *projectWalkState) collectFile(
	entry fs.DirEntry,
	relative string,
) error {
	if !entry.Type().IsRegular() ||
		isHardExcludedFile(relative) ||
		state.matcher.MatchesPath(relative) {
		return nil
	}
	if len(state.context) > 0 {
		included, err := matchesPattern(state.context, relative)
		if err != nil || !included {
			return err
		}
	}
	excluded, err := matchesPattern(state.exclude, relative)
	if err != nil || excluded {
		return err
	}
	state.paths = append(state.paths, relative)
	return nil
}

func readIgnorePatterns(
	directory string,
	domain string,
) ([]string, error) {
	file, err := os.Open(filepath.Join(directory, ".gitignore"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		patterns = append(patterns, scopedIgnorePattern(line, domain))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return patterns, nil
}

func scopedIgnorePattern(pattern string, domain string) string {
	if domain == "" {
		return pattern
	}
	prefix := ""
	if strings.HasPrefix(pattern, "!") {
		prefix = "!"
		pattern = strings.TrimPrefix(pattern, "!")
	}
	if strings.HasPrefix(pattern, `\!`) || strings.HasPrefix(pattern, `\#`) {
		return prefix + domain + "/**/" + pattern
	}
	if strings.HasPrefix(pattern, "/") {
		return prefix + domain + pattern
	}
	withoutTrailingSlash := strings.TrimSuffix(pattern, "/")
	if strings.Contains(withoutTrailingSlash, "/") {
		return prefix + domain + "/" + pattern
	}
	return prefix + domain + "/**/" + pattern
}

func matchesPattern(patterns []string, path string) (bool, error) {
	for _, pattern := range patterns {
		matches, err := doublestar.Match(filepath.ToSlash(pattern), path)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}

var secretFileNames = map[string]struct{}{
	".env":       {},
	".npmrc":     {},
	".pypirc":    {},
	"id_rsa":     {},
	"id_ed25519": {},
}

var secretFileSuffixes = []string{".key", ".pem", ".p12", ".pfx"}

func isHardExcludedFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if _, excluded := secretFileNames[name]; excluded {
		return true
	}
	if strings.HasPrefix(name, ".env.") {
		return true
	}
	for _, suffix := range secretFileSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
