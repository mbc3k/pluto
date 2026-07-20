package cache

import (
	"testing"

	"github.com/mbc3k/pluto/internal/pluto"
)

func TestM3UInvalidatedOnSetAll(t *testing.T) {
	c := New(2)
	c.SetAll([]pluto.Channel{{ID: "1", Name: "A"}}, []byte("<tv/>"))
	c.SetM3U(0, 1, "#EXTM3U\nold\n")

	if got, ok := c.GetM3U(0, 1); !ok || got != "#EXTM3U\nold\n" {
		t.Fatalf("expected cache hit, got ok=%v %q", ok, got)
	}
	// Wrong token gen → miss.
	if _, ok := c.GetM3U(0, 2); ok {
		t.Fatal("expected miss for different token gen")
	}

	// Channel refresh must invalidate M3Us.
	c.SetAll([]pluto.Channel{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}, []byte("<tv/>"))
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
}
