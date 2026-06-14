package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const cacheFileName = "rankings_cache.json"
const cacheMaxAge = 24 * time.Hour // Cache is valid for 24 hours

// RankingsCache provides persistent disk caching for benchmark results.
// It saves the last known good model rankings to a JSON file so the proxy
// can start instantly with known-good data, then refresh in the background.
type RankingsCache struct {
	mu       sync.RWMutex
	filePath string
}

// cacheData is the JSON structure stored on disk.
type cacheData struct {
	SavedAt time.Time    `json:"saved_at"`
	Models  RankedModels `json:"models"`
}

// NewRankingsCache creates a new cache instance.
func NewRankingsCache(dir string) *RankingsCache {
	path := filepath.Join(dir, cacheFileName)
	return &RankingsCache{filePath: path}
}

// Save persists the current model rankings to disk.
// This is called after each successful benchmark cycle.
func (c *RankingsCache) Save(models RankedModels) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := cacheData{
		SavedAt: time.Now(),
		Models:  models,
	}

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically: write to temp file, then rename
	tmpPath := c.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonBytes, 0644); err != nil {
		return err
	}

	// On Windows, rename requires the destination to not exist
	os.Remove(c.filePath)
	if err := os.Rename(tmpPath, c.filePath); err != nil {
		// Fallback: just write directly
		return os.WriteFile(c.filePath, jsonBytes, 0644)
	}

	return nil
}

// Load reads the last saved model rankings from disk.
// Returns nil if no cache exists or if it's older than cacheMaxAge.
// Also returns models even if stale when allowStale=true.
func (c *RankingsCache) Load(allowStale bool) RankedModels {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return nil
	}

	var cache cacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}

	// Check freshness
	age := time.Since(cache.SavedAt)
	if age > cacheMaxAge && !allowStale {
		return nil
	}

	// Validate we have actual models
	if len(cache.Models) == 0 {
		return nil
	}

	return cache.Models
}

// Age returns how old the cache is. Returns 0 if no cache exists.
func (c *RankingsCache) Age() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info, err := os.Stat(c.filePath)
	if err != nil {
		return 0
	}
	return time.Since(info.ModTime())
}

// Exists returns whether a cache file exists.
func (c *RankingsCache) Exists() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, err := os.Stat(c.filePath)
	return err == nil
}
