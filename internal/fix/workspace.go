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

func mirrorProject(
	root string,
	workspace string,
	options Options,
) (map[string][]byte, error) {
	required, err := requiredProjectPaths(root, options)
	if err != nil {
		return nil, err
	}
	paths, err := discoverProjectPaths(root, options.Context, options.Exclude)
	if err != nil {
		return nil, err
	}
	for path := range required {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	before := make(map[string][]byte, len(paths))
	for _, relative := range paths {
		relative = filepath.ToSlash(relative)
		if _, exists := before[relative]; exists {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(relative)) {
			return nil, fmt.Errorf("project path escapes root: %q", relative)
		}
		sourcePath := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("project file %q is not a regular file", relative)
		}
		if isHardExcludedFile(relative) {
			if _, ok := required[relative]; ok {
				return nil, fmt.Errorf(
					"required fix file %q is excluded by the safety policy",
					relative,
				)
			}
			continue
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", relative, err)
		}
		destination := filepath.Join(workspace, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return nil, fmt.Errorf("create directory for %q: %w", relative, err)
		}
		if err := os.WriteFile(destination, content, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("mirror %q: %w", relative, err)
		}
		before[relative] = content
	}
	return before, nil
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
	if options.ConfigPath == "" {
		return required, nil
	}
	absoluteConfig, err := filepath.Abs(options.ConfigPath)
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
	var patterns []string
	matcher := ignore.CompileIgnoreLines()
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(
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
			rootPatterns, readErr := readIgnorePatterns(path, "")
			patterns = append(patterns, rootPatterns...)
			matcher = ignore.CompileIgnoreLines(patterns...)
			return readErr
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if isHardExcludedDirectory(entry.Name()) ||
				matcher.MatchesPath(relative+"/") {
				return filepath.SkipDir
			}
			nested, readErr := readIgnorePatterns(path, relative)
			patterns = append(patterns, nested...)
			matcher = ignore.CompileIgnoreLines(patterns...)
			return readErr
		}
		if !entry.Type().IsRegular() ||
			isHardExcludedFile(relative) ||
			matcher.MatchesPath(relative) {
			return nil
		}
		included, err := matchesAny(contextPatterns, relative, len(contextPatterns) == 0)
		if err != nil || !included {
			return err
		}
		excluded, err := matchesAny(excludePatterns, relative, false)
		if err != nil || excluded {
			return err
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover fix context: %w", err)
	}
	return paths, nil
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

func matchesAny(patterns []string, path string, fallback bool) (bool, error) {
	for _, pattern := range patterns {
		matches, err := doublestar.Match(filepath.ToSlash(pattern), path)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}
	return fallback, nil
}

func isHardExcludedDirectory(name string) bool {
	_, excluded := excludedDirectoryNames[name]
	return excluded
}

func isHardExcludedFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == ".env" ||
		strings.HasPrefix(name, ".env.") ||
		name == ".npmrc" ||
		name == ".pypirc" ||
		name == "id_rsa" ||
		name == "id_ed25519" ||
		strings.HasSuffix(name, ".key") ||
		strings.HasSuffix(name, ".pem") ||
		strings.HasSuffix(name, ".p12") ||
		strings.HasSuffix(name, ".pfx")
}
