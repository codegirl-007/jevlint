package scoping

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"jevlint/internal/config"
)

func Applies(rule config.Rule, path string) (bool, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "./")

	included := len(rule.Include) == 0
	for _, pattern := range rule.Include {
		matches, err := doublestar.Match(filepath.ToSlash(pattern), path)
		if err != nil {
			return false, fmt.Errorf("rule %q include pattern %q: %w", rule.ID, pattern, err)
		}
		if matches {
			included = true
			break
		}
	}
	if !included {
		return false, nil
	}

	for _, pattern := range rule.Exclude {
		matches, err := doublestar.Match(filepath.ToSlash(pattern), path)
		if err != nil {
			return false, fmt.Errorf("rule %q exclude pattern %q: %w", rule.ID, pattern, err)
		}
		if matches {
			return false, nil
		}
	}
	return true, nil
}
