package cache

import (
	"fmt"
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/pluto"
)

// m3uSlot holds a generated playlist that is valid only for a specific
// (token generation, channel-data generation) pair.
type m3uSlot struct {
	data        string
	tokenGen    int64
	channelsGen uint64
}

// Cache is a thread-safe in-memory store for the XMLTV EPG document, the
// channel list, and lazily-built per-tuner M3U playlists. Reads are served
// from the last successful refresh; a failed refresh never clears the
// existing data (serve stale, never dark).
type Cache struct {
	mu         sync.RWMutex
	m3u        []m3uSlot // indexed 0..TunerCount-1
	channels   []pluto.Channel
	xmltv      []byte
	updated    time.Time
	generation uint64 // increments on every accepted SetAll
}

// New creates an empty Cache pre-allocated for the given tuner count.
func New(tunerCount int) *Cache {
	return &Cache{
		m3u: make([]m3uSlot, tunerCount),
	}
}

// SetAll atomically replaces channel and EPG content and invalidates every
// cached M3U. It refuses an empty channel list or EPG document so a bad
// payload cannot wipe the last good snapshot.
func (c *Cache) SetAll(channels []pluto.Channel, xmltv []byte) error {
	if len(channels) == 0 || len(xmltv) == 0 {
		return fmt.Errorf("refusing empty guide update (channels=%d xmltv=%d)", len(channels), len(xmltv))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels = channels
	c.xmltv = xmltv
	c.updated = time.Now()
	c.generation++
	for i := range c.m3u {
		c.m3u[i] = m3uSlot{}
	}
	return nil
}

// GetM3U returns a previously stored playlist for the given tuner if it was
// built with the same token generation and against the current channel data.
// Returns ("", false) on miss so the caller can regenerate.
func (c *Cache) GetM3U(tuner int, tokenGen int64) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.generation == 0 || tuner < 0 || tuner >= len(c.m3u) {
		return "", false
	}
	slot := c.m3u[tuner]
	if slot.data == "" || slot.tokenGen != tokenGen || slot.channelsGen != c.generation {
		return "", false
	}
	return slot.data, true
}

// SetM3U stores a generated playlist for the given tuner, tagged with the
// token generation it embeds and the channel generation it was built from.
// It returns false without storing when that channel generation is no longer
// current, or when doing so would replace a newer token's playlist.
func (c *Cache) SetM3U(tuner int, tokenGen int64, channelsGen uint64, data string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tuner < 0 || tuner >= len(c.m3u) || c.generation == 0 || channelsGen != c.generation || data == "" {
		return false
	}
	slot := c.m3u[tuner]
	if slot.data != "" && slot.channelsGen == channelsGen && slot.tokenGen > tokenGen {
		return false
	}
	c.m3u[tuner] = m3uSlot{
		data:        data,
		tokenGen:    tokenGen,
		channelsGen: channelsGen,
	}
	return true
}

// GetXMLTV returns the XMLTV EPG bytes and the time they were stored.
// Returns (nil, zero, false) if the cache has not been populated yet.
func (c *Cache) GetXMLTV() ([]byte, time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() {
		return nil, time.Time{}, false
	}
	return c.xmltv, c.updated, true
}

// GetChannels returns the cached channel list and the generation it belongs to.
// Returns (nil, 0, false) if the cache has not been populated yet.
func (c *Cache) GetChannels() ([]pluto.Channel, uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.updated.IsZero() {
		return nil, 0, false
	}
	return c.channels, c.generation, true
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
