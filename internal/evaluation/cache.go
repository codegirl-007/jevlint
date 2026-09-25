package evaluation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const cacheEntryVersion = 1

type ResultCache interface {
	Get(string) (map[string]Result, bool)
	Put(string, map[string]Result) bool
	Clear() error
}

type FileCache struct {
	root string
}

type cacheEntry struct {
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"createdAt"`
	Results   map[string]Result `json:"results"`
}

func NewFileCache(projectRoot string) (*FileCache, error) {
	userCache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache directory: %w", err)
	}
	absoluteRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project cache scope: %w", err)
	}
	scope := sha256.Sum256([]byte(filepath.Clean(absoluteRoot)))
	return newFileCacheAt(filepath.Join(
		userCache,
		"jevlint",
		fmt.Sprintf("v%d", cacheEntryVersion),
		fmt.Sprintf("%x", scope),
	))
}

func newFileCacheAt(root string) (*FileCache, error) {
	cache := &FileCache{root: root}
	if err := cache.ensureRoot(); err != nil {
		return nil, err
	}
	return cache, nil
}

func (cache *FileCache) Get(key string) (map[string]Result, bool) {
	path := cache.entryPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil ||
		entry.Version != cacheEntryVersion ||
		len(entry.Results) == 0 ||
		!validCachedResults(entry.Results) {
		_ = os.Remove(path)
		return nil, false
	}
	return cloneResults(entry.Results), true
}

func (cache *FileCache) Put(key string, results map[string]Result) bool {
	if len(results) == 0 || !validCachedResults(results) {
		return false
	}
	if err := cache.ensureRoot(); err != nil {
		return false
	}
	data, err := json.Marshal(cacheEntry{
		Version:   cacheEntryVersion,
		CreatedAt: time.Now().UTC(),
		Results:   results,
	})
	if err != nil {
		return false
	}

	temporary, err := os.CreateTemp(cache.root, ".write-*")
	if err != nil {
		return false
	}
	temporaryPath := temporary.Name()
	succeeded := false
	defer func() {
		_ = temporary.Close()
		if !succeeded {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return false
	}
	if _, err := temporary.Write(data); err != nil {
		return false
	}
	if err := temporary.Sync(); err != nil {
		return false
	}
	if err := temporary.Close(); err != nil {
		return false
	}
	if err := os.Rename(temporaryPath, cache.entryPath(key)); err != nil {
		return false
	}
	succeeded = true
	return true
}

func (cache *FileCache) Clear() error {
	if err := os.RemoveAll(cache.root); err != nil {
		return fmt.Errorf("clear evaluation cache: %w", err)
	}
	return cache.ensureRoot()
}

func (cache *FileCache) ensureRoot() error {
	if err := os.MkdirAll(cache.root, 0o700); err != nil {
		return fmt.Errorf("create evaluation cache: %w", err)
	}
	if err := os.Chmod(cache.root, 0o700); err != nil {
		return fmt.Errorf("secure evaluation cache: %w", err)
	}
	return nil
}

func (cache *FileCache) entryPath(key string) string {
	return filepath.Join(cache.root, key+".json")
}

func validCachedResults(results map[string]Result) bool {
	for _, result := range results {
		if result.Validate() != nil {
			return false
		}
	}
	return true
}

func cloneResults(results map[string]Result) map[string]Result {
	cloned := make(map[string]Result, len(results))
	for ruleID, result := range results {
		cloned[ruleID] = result
	}
	return cloned
}
