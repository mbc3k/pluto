package cache

import (
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/pluto"
)

// m3uSlot holds a generated playlist that is valid only for a specific
// (token generation, channel-data generation) pair.
type m3uSlot struct {
	data         string
	tokenGen     int64
	channelsAt   time.Time // equals Cache.updated when the M3U was built
}

// Cache is a thread-safe in-memory store for the XMLTV EPG document, the
// channel list, and lazily-built per-tuner M3U playlists. Reads are served
// from the last successful refresh; a failed refresh never clears the
// existing data (serve stale, never dark).
type Cache struct {
	mu       sync.RWMutex
	m3u      []m3uSlot // indexed 0..TunerCount-1
	channels []pluto.Channel
	xmltv    []byte
	updated  time.Time
}

// New creates an empty Cache pre-allocated for the given tuner count.
func New(tunerCount int) *Cache {
	return &Cache{
		m3u: make([]m3uSlot, tunerCount),
	}
}

// SetAll atomically replaces channel and EPG content and invalidates every
// cached M3U (they embed channel IDs/metadata that may have changed).
func (c *Cache) SetAll(channels []pluto.Channel, xmltv []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels = channels
	c.xmltv = xmltv
	c.updated = time.Now()
	for i := range c.m3u {
		c.m3u[i] = m3uSlot{}
	}
}

// GetM3U returns a previously stored playlist for the given tuner if it was
// built with the same token generation and against the current channel data.
// Returns ("", false) on miss so the caller can regenerate.
func (c *Cache) GetM3U(tuner int, tokenGen int64) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() || tuner < 0 || tuner >= len(c.m3u) {
		return "", false
	}
	slot := c.m3u[tuner]
	if slot.data == "" || slot.tokenGen != tokenGen || !slot.channelsAt.Equal(c.updated) {
		return "", false
	}
	return slot.data, true
}

// SetM3U stores a generated playlist for the given tuner, tagged with the
// token generation it embeds and the current channel-data generation.
func (c *Cache) SetM3U(tuner int, tokenGen int64, data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tuner < 0 || tuner >= len(c.m3u) || c.updated.IsZero() {
		return
	}
	c.m3u[tuner] = m3uSlot{
		data:       data,
		tokenGen:   tokenGen,
		channelsAt: c.updated,
	}
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

// GetChannels returns the cached channel list.
// Returns (nil, false) if the cache has not been populated yet.
func (c *Cache) GetChannels() ([]pluto.Channel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() {
		return nil, false
	}
	return c.channels, true
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
