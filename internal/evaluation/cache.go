package evaluation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	cacheEntryVersion   = 1
	cacheEntryExtension = ".json"
)

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

func NewFileCache(
	projectRoot string,
	userCacheDir func() (string, error),
) (*FileCache, error) {
	userCache, err := userCacheDir()
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
	path := filepath.Join(cache.root, key+cacheEntryExtension)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	entry, err := decodeCacheEntry(data)
	if err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, false
		}
		return nil, false
	}
	return cloneResults(entry.Results), true
}

func decodeCacheEntry(data []byte) (cacheEntry, error) {
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, fmt.Errorf("decode cache entry: %w", err)
	}
	if entry.Version != cacheEntryVersion {
		return cacheEntry{}, fmt.Errorf("unsupported cache entry version %d", entry.Version)
	}
	if len(entry.Results) == 0 {
		return cacheEntry{}, fmt.Errorf("invalid cache entry results")
	}
	for _, result := range entry.Results {
		if err := result.Validate(); err != nil {
			return cacheEntry{}, fmt.Errorf("invalid cache entry results")
		}
	}
	return entry, nil
}

func (cache *FileCache) Put(key string, results map[string]Result) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if err := result.Validate(); err != nil {
			return false
		}
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
	return writeAtomicCacheFile(
		cache.root,
		filepath.Join(cache.root, key+cacheEntryExtension),
		data,
	) == nil
}

func writeAtomicCacheFile(directory string, destination string, data []byte) error {
	temporary, err := os.CreateTemp(directory, ".write-*")
	if err != nil {
		return err
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
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	succeeded = true
	return nil
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

func cloneResults(results map[string]Result) map[string]Result {
	cloned := make(map[string]Result, len(results))
	for ruleID, result := range results {
		cloned[ruleID] = result
	}
	return cloned
}
