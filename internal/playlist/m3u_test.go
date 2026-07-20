package playlist

import (
	"strings"
	"testing"

	"github.com/mbc3k/pluto/internal/pluto"
)

func TestGenerate_embedsCredentialsOncePerChannel(t *testing.T) {
	channels := []pluto.Channel{
		{ID: "id-a", Name: "Alpha", Slug: "alpha", Category: "News", ColorLogoPNG: pluto.ImageRef{Path: "http://logo/a.png"}},
		{ID: "id-b", Name: `Beta "HD"`, Slug: "beta", Category: "Movies", ColorLogoPNG: pluto.ImageRef{Path: "http://logo/b.png"}},
	}

	out := Generate(1000, "jwt-token", "deviceId=abc&sid=1", channels)

	if !strings.HasPrefix(out, "#EXTM3U\n") {
		t.Fatalf("missing header: %q", out[:min(20, len(out))])
	}
	if strings.Count(out, "jwt=jwt-token") != 2 {
		t.Fatalf("expected jwt once per channel, got:\n%s", out)
	}
	if !strings.Contains(out, "deviceId=abc&sid=1&jwt=jwt-token&masterJWTPassthrough=true&includeExtendedEvents=true") {
		t.Fatalf("missing stitcher query suffix:\n%s", out)
	}
	// %q must escape the quote in the channel name.
	if !strings.Contains(out, `tvg-name="Beta \"HD\""`) {
		t.Fatalf("channel name not properly quoted:\n%s", out)
	}
	if !strings.Contains(out, "/channel/id-a/master.m3u8?") {
		t.Fatalf("missing stream path for id-a:\n%s", out)
	}
	if !strings.Contains(out, "tvg-chno=1000") || !strings.Contains(out, "tvg-chno=1001") {
		t.Fatalf("channel numbers wrong:\n%s", out)
	}
}

func TestGenerate_emptyChannels(t *testing.T) {
	out := Generate(1, "t", "", nil)
	if out != "#EXTM3U\n" {
		t.Fatalf("got %q", out)
	}
}
