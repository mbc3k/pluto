package cache

import (
	"sync"
	"time"
)

// Cache is a thread-safe in-memory store for generated M3U playlists and
// the XMLTV EPG document. Reads are served from the last successful refresh;
// a failed refresh never clears the existing data (serve stale, never dark).
type Cache struct {
	mu      sync.RWMutex
	m3u     []string // indexed 0..TunerCount-1
	xmltv   []byte
	updated time.Time
}

// New creates an empty Cache pre-allocated for the given tuner count.
func New(tunerCount int) *Cache {
	return &Cache{
		m3u: make([]string, tunerCount),
	}
}

// SetAll atomically replaces all cached content. m3u must have the same
// length as the tunerCount passed to New.
func (c *Cache) SetAll(m3u []string, xmltv []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m3u = m3u
	c.xmltv = xmltv
	c.updated = time.Now()
}

// GetM3U returns the M3U playlist for the given tuner index (0-based).
// Returns ("", false) if the cache has not been populated yet or the
// index is out of range.
func (c *Cache) GetM3U(tuner int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() || tuner < 0 || tuner >= len(c.m3u) {
		return "", false
	}
	return c.m3u[tuner], true
}

// GetXMLTV returns the XMLTV EPG bytes.
// Returns (nil, false) if the cache has not been populated yet.
func (c *Cache) GetXMLTV() ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() {
		return nil, false
	}
	return c.xmltv, true
}

// LastUpdated returns the time of the last successful SetAll call,
// or the zero value if the cache has never been populated.
func (c *Cache) LastUpdated() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updated
}

// IsReady reports whether the cache has been populated at least once.
func (c *Cache) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.updated.IsZero()
}
