package tve

import (
	"strings"
	"testing"
	"time"
)

func TestParseAESchedule(t *testing.T) {
	body := []byte(`[
		{"timeslotId":"slot-1","startTime":1700000000000,"endTime":1700003600000,"series":{"title":"Ancient Aliens"},"program":{"title":"The Episode","description":"About it"}},
		{"timeslotId":"slot-1","startTime":1,"endTime":2,"series":{"title":"Duplicate"}},
		{"id":12,"startTime":1700003600000,"endTime":1700007200000,"parent":{"title":"Parent Only"}},
		{"timeslotId":"bad","startTime":5,"endTime":5,"series":{"title":"Zero"}}
	]`)
	got, err := ParseAESchedule("history", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len %d %#v", len(got), got)
	}
	if got[0].Title != "Ancient Aliens" || got[0].Subtitle != "The Episode" || got[0].Desc != "About it" {
		t.Fatalf("first %#v", got[0])
	}
	if !got[0].Start.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Fatalf("start %s", got[0].Start)
	}
	if got[1].Title != "Parent Only" || got[1].Subtitle != "" {
		t.Fatalf("second %#v", got[1])
	}
}

func TestParseAENowPlaying(t *testing.T) {
	body := []byte(`{"data":{"nowPlaying":{"startTime":1700010000,"endTime":1700013600,"program":{"__typename":"Episode","title":"Live Ep","description":"live","series":{"title":"Live Show"}}}}}`)
	got, ok := ParseAENowPlaying(body)
	if !ok || got.Title != "Live Show" || got.Subtitle != "Live Ep" || got.Desc != "live" {
		t.Fatalf("%v %#v", ok, got)
	}
	if !got.Start.Equal(time.Unix(1700010000, 0).UTC()) {
		t.Fatalf("start %s", got.Start)
	}
	movie := []byte(`{"data":{"nowPlaying":{"startTime":10,"endTime":20,"program":{"__typename":"Movie","title":"A Movie","description":"d"}}}}`)
	got, ok = ParseAENowPlaying(movie)
	if !ok || got.Title != "A Movie" || got.Subtitle != "" {
		t.Fatalf("movie %v %#v", ok, got)
	}
	if _, ok := ParseAENowPlaying([]byte(`{"data":{}}`)); ok {
		t.Fatal("expected empty now playing to be skipped")
	}
}

func TestParseWarnerWidgetEastAndWest(t *testing.T) {
	body := []byte(`{"monday":[
		{"gmt":"Mon Aug 03 06:04:00 GMT 2026","pacific":"Mon Aug 03 06:04:00 PDT 2026","slotLength":30,"show":{"programTitle":"The Closer","seriesDesc":"series","episode":{"episodeTitle":"Pilot","description":"desc"}}},
		{"gmt":"Mon Aug 03 06:04:00 GMT 2026","pacific":"Mon Aug 03 06:04:00 PDT 2026","slotLength":30,"show":{"programTitle":"The Closer","episode":{"episodeTitle":"Pilot","description":"desc"}}},
		{"gmt":"not a time","slotLength":30,"show":{"programTitle":"Skipped"}}
	]}`)
	east, err := ParseWarnerWidget("tnt-east", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(east) != 1 || east[0].Title != "The Closer" || east[0].Subtitle != "Pilot" || east[0].Desc != "desc" {
		t.Fatalf("east %#v", east)
	}
	if east[0].Start.Format(time.RFC3339) != "2026-08-03T06:04:00Z" || east[0].Stop.Sub(east[0].Start) != 30*time.Minute {
		t.Fatalf("east times %s %s", east[0].Start, east[0].Stop)
	}
	west, err := ParseWarnerWidget("tnt-west", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(west) != 1 {
		t.Fatalf("west %#v", west)
	}
	if west[0].Start.Format(time.RFC3339) != "2026-08-03T13:04:00Z" {
		if _, err := time.LoadLocation("America/Los_Angeles"); err != nil {
			t.Skip("timezone data is not installed")
		}
		t.Fatalf("west start %s", west[0].Start.Format(time.RFC3339))
	}
	if strings.Contains(east[0].Desc, "GMT") {
		t.Fatal("timezone token leaked into the description")
	}
}
