package source

import (
	"strings"
	"testing"
	"time"
)

func TestM3UAndXMLTV(t *testing.T) {
	feed := Feed{
		ID:   "fox",
		Name: "FOX",
		Channels: []Channel{
			{ID: "news", Name: "Bad\nName", Category: "News", StreamURL: "https://example.com/live.m3u8"},
			{ID: "sports", Name: "FS1", StreamURL: "/tve/fox/sports/stream", Programs: []Program{
				{Title: "A & B", Start: time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC), Stop: time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)},
				{Title: " ", Start: time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC), Stop: time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)},
			}},
		},
	}
	m3u := M3U(feed, 20000, "http://localhost:8080/")
	if strings.Count(m3u, "\n") != 5 {
		t.Fatalf("newline leaked into a playlist line:\n%s", m3u)
	}
	if !strings.Contains(m3u, `tvg-name="Bad Name"`) || !strings.Contains(m3u, "tvg-chno=20000") || !strings.Contains(m3u, "tvg-chno=20001") {
		t.Fatalf("playlist:\n%s", m3u)
	}
	if !strings.Contains(m3u, "https://example.com/live.m3u8") || !strings.Contains(m3u, "http://localhost:8080/tve/fox/sports/stream") {
		t.Fatalf("streams:\n%s", m3u)
	}
	if !strings.Contains(m3u, `group-title="News"`) || !strings.Contains(m3u, `group-title="FOX"`) {
		t.Fatalf("groups:\n%s", m3u)
	}
	xml, err := XMLTV(feed)
	if err != nil {
		t.Fatal(err)
	}
	text := string(xml)
	if !strings.Contains(text, `generator-info-name="pluto-tve"`) || !strings.Contains(text, "A &amp; B") {
		t.Fatalf("xml:\n%s", text)
	}
	if strings.Count(text, "<programme") != 1 {
		t.Fatalf("blank title should be omitted:\n%s", text)
	}
	if !strings.Contains(text, `start="20260803060000 +0000"`) {
		t.Fatalf("start:\n%s", text)
	}
}
