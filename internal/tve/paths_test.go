package tve

import "testing"

func TestProtectedPath(t *testing.T) {
	ok := []string{
		"/tve/directv",
		"/tve/directv/code",
		"/tve/fox.m3u",
		"/tve/all.xml",
		"/tve/amcn/amc/stream",
	}
	for _, path := range ok {
		if !ProtectedPath(path) {
			t.Fatalf("expected protected %s", path)
		}
	}
	open := []string{
		"/health",
		"/status",
		"/",
		"/epg.xml",
		"/tuner-1-playlist.m3u",
		"/tve/fox.m3u/extra",
		"/tve/../epg.xml",
		"/tve/amcn/amc",
		"/tve/",
	}
	for _, path := range open {
		if ProtectedPath(path) {
			t.Fatalf("expected open %s", path)
		}
	}
}
