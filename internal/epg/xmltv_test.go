package epg

import (
	"strings"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/pluto"
)

func TestGenerateSeriesFilmAndEscaping(t *testing.T) {
	loc := time.FixedZone("X", 5*3600)
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, loc) // 2024-01-01 22:04:05 UTC
	stop := start.Add(30 * time.Minute)

	channels := []pluto.Channel{
		{
			ID:           "ch1",
			Name:         "A & B <C>",
			Slug:         "alpha",
			Category:     "News",
			ColorLogoPNG: pluto.ImageRef{Path: "http://logo/a.png"},
			Timelines: []pluto.Program{
				{
					ID:    "p1",
					Title: "Show \x00Name",
					Start: start,
					Stop:  stop,
					Episode: pluto.Episode{
						Name:        "Pilot",
						Description: "Line",
						Season:      1,
						Number:      2,
						Genre:       "News",
						SubGenre:    "News",
						Rating:      "TV-PG",
						Series:      pluto.Series{Type: "series"},
					},
				},
				{
					// Zero-length airing is not a programme.
					ID:    "bad",
					Title: "Nope",
					Start: start,
					Stop:  start,
				},
			},
		},
		{
			ID:       "ch2",
			Name:     "",
			Slug:     "movies",
			Category: "Movies",
			Timelines: []pluto.Program{
				{
					Title: "",
					Start: start,
					Stop:  stop,
					Episode: pluto.Episode{
						Name:   "The Film",
						Season: 1,
						Number: 1,
						Genre:  "Drama",
						Series: pluto.Series{Type: "film"},
					},
				},
			},
		},
	}

	out, err := Generate(channels)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)
	if !strings.Contains(doc, `encoding="UTF-8"`) {
		t.Fatalf("missing xml header:\n%s", doc)
	}
	if !strings.Contains(doc, "A &amp; B &lt;C&gt;") {
		t.Fatalf("channel name not escaped:\n%s", doc)
	}
	if strings.Contains(doc, "\x00") {
		t.Fatal("NUL survived into XML")
	}
	if !strings.Contains(doc, "Show Name") {
		t.Fatalf("stripped title missing:\n%s", doc)
	}
	if !strings.Contains(doc, "20240101220405 +0000") {
		t.Fatalf("time not converted to UTC:\n%s", doc)
	}
	if !strings.Contains(doc, `<episode-num system="onscreen">S01E02</episode-num>`) {
		t.Fatalf("missing episode number:\n%s", doc)
	}
	if strings.Count(doc, `<category lang="en">News</category>`) != 1 {
		t.Fatalf("genre duplicated:\n%s", doc)
	}
	if !strings.Contains(doc, `http://logo/a.png`) {
		t.Fatalf("missing icon:\n%s", doc)
	}
	if strings.Contains(doc, "<icon") && strings.Count(doc, "<icon") != 1 {
		t.Fatalf("unexpected icons:\n%s", doc)
	}
	if !strings.Contains(doc, `<category lang="en">Movie</category>`) {
		t.Fatalf("film missing Movie category:\n%s", doc)
	}
	if strings.Contains(doc, "S01E01") {
		t.Fatalf("film should not get an episode number:\n%s", doc)
	}
	if !strings.Contains(doc, ">movies</display-name>") {
		t.Fatalf("empty channel name should fall back to slug:\n%s", doc)
	}
	if strings.Count(doc, "<programme ") != 2 {
		t.Fatalf("zero-length programme was emitted:\n%s", doc)
	}
	if !strings.Contains(doc, "The Film") {
		t.Fatalf("film title fallback missing:\n%s", doc)
	}
	if !strings.Contains(doc, "TV-PG") {
		t.Fatalf("rating missing:\n%s", doc)
	}
}
