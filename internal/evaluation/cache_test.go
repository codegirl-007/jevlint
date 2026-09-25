package evaluation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileCacheRoundTripUsesPrivateAtomicStorage(t *testing.T) {
	t.Parallel()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	results := map[string]Result{
		"rule": {Status: StatusFail, Confidence: 0.9},
	}
	if !cache.Put("key", results) {
		t.Fatal("Put() = false")
	}

	rootInfo, err := os.Stat(cache.root)
	if err != nil {
		t.Fatalf("stat cache root: %v", err)
	}
	if permissions := rootInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("cache root permissions = %o, want 700", permissions)
	}
	entryInfo, err := os.Stat(cache.entryPath("key"))
	if err != nil {
		t.Fatalf("stat cache entry: %v", err)
	}
	if permissions := entryInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("cache entry permissions = %o, want 600", permissions)
	}

	data, err := os.ReadFile(cache.entryPath("key"))
	if err != nil {
		t.Fatalf("read cache entry: %v", err)
	}
	if strings.Contains(string(data), "source") {
		t.Fatalf("cache entry contains source data: %s", data)
	}
	temporary, err := filepath.Glob(filepath.Join(cache.root, ".write-*"))
	if err != nil {
		t.Fatalf("glob temporary entries: %v", err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary cache entries = %v", temporary)
	}

	got, ok := cache.Get("key")
	if !ok || got["rule"] != results["rule"] {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	got["rule"] = Result{Status: StatusPass, Confidence: 1}
	again, ok := cache.Get("key")
	if !ok || again["rule"].Status != StatusFail {
		t.Fatalf("Get() returned shared results: %#v, %v", again, ok)
	}
}

func TestFileCacheRejectsCorruptInvalidAndOldEntries(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"corrupt": `not json`,
		"old version": `{
			"version": 0,
			"results": {"rule": {"status": "pass", "confidence": 1}}
		}`,
		"invalid result": `{
			"version": 1,
			"results": {"rule": {"status": "maybe", "confidence": 1}}
		}`,
		"empty results": `{
			"version": 1,
			"results": {}
		}`,
	}
	for name, contents := range tests {
		name, contents := name, contents
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatalf("newFileCacheAt() error = %v", err)
			}
			path := cache.entryPath("key")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write cache entry: %v", err)
			}
			if results, ok := cache.Get("key"); ok || results != nil {
				t.Fatalf("Get() = %#v, %v; want miss", results, ok)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid cache entry still exists: %v", err)
			}
		})
	}
}

func TestFileCacheScopesAndClearsEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, err := newFileCacheAt(filepath.Join(root, "first"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	second, err := newFileCacheAt(filepath.Join(root, "second"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	results := map[string]Result{
		"rule": {Status: StatusPass, Confidence: 1},
	}
	if !first.Put("same-key", results) {
		t.Fatal("first Put() = false")
	}
	if _, ok := second.Get("same-key"); ok {
		t.Fatal("second project cache read first project entry")
	}
	if err := first.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, ok := first.Get("same-key"); ok {
		t.Fatal("Get() hit after Clear()")
	}
	if _, err := os.Stat(first.root); err != nil {
		t.Fatalf("cache root missing after Clear(): %v", err)
	}
}

func TestNewFileCacheScopesByAbsoluteProjectRoot(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	firstProject := t.TempDir()
	secondProject := t.TempDir()
	first, err := NewFileCache(firstProject)
	if err != nil {
		t.Fatalf("NewFileCache() error = %v", err)
	}
	same, err := NewFileCache(filepath.Join(firstProject, "."))
	if err != nil {
		t.Fatalf("NewFileCache() error = %v", err)
	}
	second, err := NewFileCache(secondProject)
	if err != nil {
		t.Fatalf("NewFileCache() error = %v", err)
	}
	if first.root != same.root {
		t.Fatalf("same project roots differ: %q and %q", first.root, same.root)
	}
	if first.root == second.root {
		t.Fatalf("different projects share cache root %q", first.root)
	}
	if !strings.HasPrefix(first.root, cacheHome) {
		t.Fatalf("cache root = %q, want it under %q", first.root, cacheHome)
	}
}
