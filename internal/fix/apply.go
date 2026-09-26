package fix

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

func Apply(root string, changes []FileChange) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	targets := make([]appliedFile, 0, len(changes))
	for _, change := range changes {
		target, err := prepareApply(root, change)
		if err != nil {
			return err
		}
		targets = append(targets, target)
	}
	for _, target := range targets {
		if err := os.WriteFile(target.path, target.after, target.perm); err != nil {
			return fmt.Errorf("apply %q: %w", target.relative, err)
		}
	}
	return nil
}

func RestoreChanges(root string, changes []FileChange) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	for _, change := range changes {
		relative := filepath.ToSlash(filepath.Clean(change.Path))
		if !filepath.IsLocal(filepath.FromSlash(relative)) {
			return fmt.Errorf("restore path escapes project root: %q", change.Path)
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		perm := os.FileMode(0o600)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			perm = info.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("restore directory for %q: %w", relative, err)
		}
		if err := os.WriteFile(path, change.Before, perm); err != nil {
			return fmt.Errorf("restore %q: %w", relative, err)
		}
	}
	return nil
}

type appliedFile struct {
	path     string
	relative string
	after    []byte
	perm     os.FileMode
}

func prepareApply(root string, change FileChange) (appliedFile, error) {
	relative := filepath.ToSlash(filepath.Clean(change.Path))
	if !filepath.IsLocal(filepath.FromSlash(relative)) {
		return appliedFile{}, fmt.Errorf(
			"apply path escapes project root: %q",
			change.Path,
		)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := requireRegularContainedFile(root, path); err != nil {
		return appliedFile{}, fmt.Errorf("apply %q: %w", relative, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return appliedFile{}, fmt.Errorf("apply %q: %w", relative, err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return appliedFile{}, fmt.Errorf("apply %q: %w", relative, err)
	}
	if !bytes.Equal(current, change.Before) {
		return appliedFile{}, fmt.Errorf(
			"apply %q: file changed since the proposal was generated",
			relative,
		)
	}
	return appliedFile{
		path:     path,
		relative: relative,
		after:    change.After,
		perm:     info.Mode().Perm(),
	}, nil
}
