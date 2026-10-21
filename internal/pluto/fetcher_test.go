package pluto

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMergeChannelWindows(t *testing.T) {
	start := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	later := start.Add(time.Hour)
	src := []Program{{
		ID:    "p1",
		Title: "Original",
		Start: start,
		Stop:  start.Add(30 * time.Minute),
	}}
	windows := [][]Channel{
		{
			{ID: "", Slug: "ghost", Name: "Ghost", IsStitched: true},
			{ID: "ann", Slug: "Announcement-Now", Name: "Announce", IsStitched: true},
			{ID: "priv", Slug: "privacy-policy", Name: "Privacy", IsStitched: true},
			{ID: "plain", Slug: "plain", Name: "Plain", IsStitched: false, Timelines: src},
			{ID: "c1", Slug: "CNN", Name: "", Number: 5, IsStitched: true, Category: "News", Timelines: src},
			{ID: "b", Slug: "beta", Name: "Beta", Number: 5, IsStitched: true},
			{ID: "a", Slug: "alpha", Name: "Alpha", Number: 5, IsStitched: true},
		},
		{
			{ID: "c1", Slug: "CNN", Name: "CNN", Number: 5, IsStitched: true, Timelines: []Program{
				{ID: "p1", Title: "Duplicate", Start: start, Stop: start.Add(30 * time.Minute)},
				{ID: "p2", Title: "Later", Start: later, Stop: later.Add(time.Hour)},
				{ID: "bad", Title: "Zero", Start: later, Stop: later},
			}},
			{ID: "late", Slug: "late-show", Name: "Late", Number: 9, IsStitched: true, Timelines: []Program{
				{ID: "only", Title: "Only", Start: later, Stop: later.Add(time.Hour)},
			}},
		},
	}

	got, err := mergeChannelWindows(windows)
	if err != nil {
		t.Fatal(err)
	}
	src[0].Title = "MUTATED"
	if len(got) != 4 {
		t.Fatalf("channels: %+v", got)
	}
	// Equal numbers sort by slug: alpha, beta, pluto-CNN, then number 9.
	wantOrder := []string{"alpha", "beta", "pluto-CNN", "late-show"}
	for i, slug := range wantOrder {
		if got[i].Slug != slug {
			t.Fatalf("order[%d]=%s, want %s (full %#v)", i, got[i].Slug, slug, slugs(got))
		}
	}
	cnn := got[2]
	if cnn.Name != "pluto-CNN" {
		t.Fatalf("empty name should fall back to prefixed slug, got %q", cnn.Name)
	}
	if cnn.Timelines[0].Title != "Original" {
		t.Fatalf("timeline title %q, want Original (copy or first-seen dedupe failed)", cnn.Timelines[0].Title)
	}
	if len(cnn.Timelines) != 2 || cnn.Timelines[0].ID != "p1" || cnn.Timelines[1].ID != "p2" {
		t.Fatalf("timelines: %+v", cnn.Timelines)
	}

	again, err := mergeChannelWindows(windows)
	if err != nil {
		t.Fatal(err)
	}
	if slugs(got)[0] != slugs(again)[0] || slugs(got)[1] != slugs(again)[1] {
		t.Fatalf("order changed across merges: %v vs %v", slugs(got), slugs(again))
	}
}

func TestMergeChannelWindowsEmpty(t *testing.T) {
	_, err := mergeChannelWindows([][]Channel{{
		{ID: "x", Slug: "announcement", IsStitched: true},
		{ID: "y", Slug: "real", IsStitched: false},
	}})
	if err == nil {
		t.Fatal("expected no usable channels")
	}
}

func TestFetchChannelsMergesFourWindows(t *testing.T) {
	var mu sync.Mutex
	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		starts = append(starts, q.Get("start"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]Channel{{
			ID: "c1", Name: "One", Slug: "one", Number: 1, IsStitched: true,
			Timelines: []Program{{
				ID: "p", Title: "Show", Start: time.Now().UTC(), Stop: time.Now().UTC().Add(time.Hour),
			}},
		}})
	}))
	defer srv.Close()

	old := channelsAPI
	channelsAPI = srv.URL
	t.Cleanup(func() { channelsAPI = old })

	got, err := FetchChannels(context.Background(), NewRetryClient())
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 4 {
		t.Fatalf("windows fetched: %v", starts)
	}
	seen := map[string]bool{}
	for _, s := range starts {
		if seen[s] {
			t.Fatalf("duplicate window start %s in %v", s, starts)
		}
		seen[s] = true
		if _, err := time.Parse("2006-01-02 15:04:05.000-0700", s); err != nil {
			t.Fatalf("start %q: %v", s, err)
		}
	}
	if len(got) != 1 || len(got[0].Timelines) != 1 {
		t.Fatalf("merged: %+v", got)
	}
}

func TestFetchWindowNon200IncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	old := channelsAPI
	channelsAPI = srv.URL
	t.Cleanup(func() { channelsAPI = old })

	_, err := fetchWindow(context.Background(), NewRetryClient(), time.Now(), time.Now().Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}

func slugs(channels []Channel) []string {
	out := make([]string, len(channels))
	for i := range channels {
		out[i] = channels[i].Slug
	}
	return out
}
