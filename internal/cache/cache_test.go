package cache

import (
	"sync"
	"testing"

	"github.com/mbc3k/pluto/internal/pluto"
)

func mustSetAll(t *testing.T, c *Cache, channels []pluto.Channel, xmltv []byte) {
	t.Helper()
	if err := c.SetAll(channels, xmltv); err != nil {
		t.Fatalf("SetAll: %v", err)
	}
}

func TestM3UInvalidatedOnSetAll(t *testing.T) {
	c := New(2)
	mustSetAll(t, c, []pluto.Channel{{ID: "1", Name: "A"}}, []byte("<tv/>"))
	_, gen, ok := c.GetChannels()
	if !ok {
		t.Fatal("expected channels")
	}
	if !c.SetM3U(0, 1, gen, "#EXTM3U\nold\n") {
		t.Fatal("expected store")
	}

	if got, ok := c.GetM3U(0, 1); !ok || got != "#EXTM3U\nold\n" {
		t.Fatalf("expected cache hit, got ok=%v %q", ok, got)
	}
	// Wrong token gen → miss.
	if _, ok := c.GetM3U(0, 2); ok {
		t.Fatal("expected miss for different token gen")
	}

	// Channel refresh must invalidate M3Us.
	mustSetAll(t, c, []pluto.Channel{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}, []byte("<tv/>"))
	if _, ok := c.GetM3U(0, 1); ok {
		t.Fatal("expected miss after SetAll")
	}
}

func TestGetM3UBeforeReady(t *testing.T) {
	c := New(1)
	if _, ok := c.GetM3U(0, 1); ok {
		t.Fatal("empty cache should miss")
	}
	if c.IsReady() {
		t.Fatal("empty cache should not be ready")
	}
	if _, _, ok := c.GetXMLTV(); ok {
		t.Fatal("empty cache should not serve xmltv")
	}
}

func TestSetAllRejectsEmptyAndKeepsPrevious(t *testing.T) {
	c := New(1)
	mustSetAll(t, c, []pluto.Channel{{ID: "1", Name: "A"}}, []byte("<tv/>"))
	if err := c.SetAll(nil, []byte("<tv/>")); err == nil {
		t.Fatal("expected empty channel list to be rejected")
	}
	if err := c.SetAll([]pluto.Channel{{ID: "1"}}, nil); err == nil {
		t.Fatal("expected empty xmltv to be rejected")
	}
	got, _, ok := c.GetChannels()
	if !ok || len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("previous channels were clobbered: ok=%v %+v", ok, got)
	}
	xml, _, ok := c.GetXMLTV()
	if !ok || string(xml) != "<tv/>" {
		t.Fatalf("previous xmltv was clobbered: ok=%v %q", ok, xml)
	}
}

func TestSetM3UIgnoresStaleGeneration(t *testing.T) {
	c := New(1)
	mustSetAll(t, c, []pluto.Channel{{ID: "1"}}, []byte("<tv/>"))
	_, gen, _ := c.GetChannels()
	mustSetAll(t, c, []pluto.Channel{{ID: "1"}, {ID: "2"}}, []byte("<tv></tv>"))
	if c.SetM3U(0, 1, gen, "#EXTM3U\nstale\n") {
		t.Fatal("stored a playlist built from a stale channel generation")
	}
	if _, ok := c.GetM3U(0, 1); ok {
		t.Fatal("stale playlist was served")
	}
}

func TestSetM3UDoesNotClobberNewerToken(t *testing.T) {
	c := New(1)
	mustSetAll(t, c, []pluto.Channel{{ID: "1"}}, []byte("<tv/>"))
	_, gen, _ := c.GetChannels()
	if !c.SetM3U(0, 2, gen, "new") {
		t.Fatal("expected store")
	}
	if c.SetM3U(0, 1, gen, "old") {
		t.Fatal("older token clobbered a newer playlist")
	}
	got, ok := c.GetM3U(0, 2)
	if !ok || got != "new" {
		t.Fatalf("got ok=%v %q", ok, got)
	}
}

func TestSetM3URejectsOutOfRange(t *testing.T) {
	c := New(1)
	mustSetAll(t, c, []pluto.Channel{{ID: "1"}}, []byte("<tv/>"))
	_, gen, _ := c.GetChannels()
	if c.SetM3U(3, 1, gen, "x") || c.SetM3U(-1, 1, gen, "x") {
		t.Fatal("out of range tuner was stored")
	}
}

func TestConcurrentM3UAccess(t *testing.T) {
	c := New(4)
	mustSetAll(t, c, []pluto.Channel{{ID: "1", Name: "A"}}, []byte("<tv/>"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				_, gen, ok := c.GetChannels()
				if !ok {
					t.Error("cache not ready")
					return
				}
				c.SetM3U(n%4, int64(j), gen, "playlist")
				c.GetM3U(n%4, int64(j))
				c.GetXMLTV()
				if j%10 == 0 {
					_ = c.SetAll([]pluto.Channel{{ID: "1", Name: "A"}}, []byte("<tv/>"))
				}
			}
		}(i)
	}
	wg.Wait()
}
