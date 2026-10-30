package tve

import (
	"strings"
	"testing"
	"time"
)

func TestParseGuideFixtures(t *testing.T) {
	warner := []byte(`<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"groupedSchedule":{"Monday":[{"gmt":"Mon Aug 03 06:04:00 GMT 2026","pacific":"Mon Aug 03 06:04:00 PDT 2026","slotLength":30,"show":{"programTitle":"Widget Show","seriesDesc":"series","episode":{"episodeTitle":"Part 1","description":"east desc"}}}]}}}}</script></html>`)
	programs, err := ParseWarnerWidget("tnt-east", warner)
	if err != nil || len(programs) != 1 || programs[0].Title != "Widget Show" || programs[0].Subtitle != "Part 1" {
		t.Fatalf("warner %+v %v", programs, err)
	}
	if programs[0].Start.UTC().Format("2006-01-02 15:04") != "2026-08-03 06:04" {
		t.Fatalf("east start %s", programs[0].Start)
	}

	tcm := []byte(`{"data":{"getScheduleEntries":[{"scheduledTimestamp":"2026-08-03T10:00:00Z","scheduledDuration":"6300","show":{"title":{"en_US":{"full":"TCM Film"}}},"summary":{"full":"a film"}}]}}`)
	programs, err = ParseTCMSchedule(tcm)
	if err != nil || len(programs) != 1 || programs[0].Title != "TCM Film" || programs[0].Desc != "a film" {
		t.Fatalf("tcm %+v %v", programs, err)
	}
	if programs[0].Stop.Sub(programs[0].Start) != 6300*time.Second {
		t.Fatalf("tcm duration %s", programs[0].Stop.Sub(programs[0].Start))
	}

	amc := []byte(`<div>{"type":"tv-listings","properties":{"scheduleItems":[{"properties":{"text":{"showTitle":"AMC Show","episodeTitle":"Ep","description":"d","startDate":"2026-08-03","startTime":"6:00PM","endDate":"2026-08-03","endTime":"7:00PM"}}}]}}</div><p>trailing</p>`)
	programs, err = ParseAMCSchedule(amc)
	if err != nil || len(programs) != 1 || programs[0].Title != "AMC Show" || programs[0].Subtitle != "Ep" {
		t.Fatalf("amc %+v %v", programs, err)
	}

	fox := []byte(`{"day":[{"show":[{"title":"Fox News Show","long-description":"long","start-utc":"2026-08-03T10:00:00Z","end-utc":"2026-08-03T11:00:00Z"}]}]}`)
	programs, err = ParseFoxNewsSchedule(fox)
	if err != nil || len(programs) != 1 || programs[0].Title != "Fox News Show" || programs[0].Desc != "long" {
		t.Fatalf("fox %+v %v", programs, err)
	}

	sports := []byte(`{"data":{"listings":{"items":[{"title":"FS1 Game","description":"ball","call_sign":"FS1","start_time":"2026-08-03T10:00:00Z","end_time":"2026-08-03T12:00:00Z"}]},"scroll":{"next":"/product/more"}}}`)
	page, err := ParseFoxSportsPage(sports, foxCallSign)
	if err != nil || len(page.ByChannel["fox_sports_fs1"]) != 1 || page.ByChannel["fox_sports_fs1"][0].Title != "FS1 Game" || page.Next != "/product/more" {
		t.Fatalf("sports %+v %v", page, err)
	}
	if foxSportsNext(page.Next) != "https://api.fox.com/fs/product/more" {
		t.Fatalf("next %s", foxSportsNext(page.Next))
	}

	discovery := []byte(`{"included":[{"type":"video","attributes":{"videoType":"LISTING","description":"Series Title","name":"Episode Name","scheduleStart":"2026-08-03T10:00:00Z","scheduleEnd":"2026-08-03T11:00:00Z"}},{"type":"video","attributes":{"videoType":"EPISODE","description":"skip me","name":"no"}}]}`)
	programs, err = ParseDiscoveryListings(discovery)
	if err != nil || len(programs) != 1 || programs[0].Title != "Series Title" || programs[0].Subtitle != "Episode Name" {
		t.Fatalf("discovery %+v %v", programs, err)
	}
}

func TestStatementFixtures(t *testing.T) {
	const stmt = "eyJhbGciOiJub25lIn0.e30.sig"
	history := []byte(`<html><script id="__NEXT_DATA__">{"props":{"config":{"adobeSoftwareStatement":{"history":"` + stmt + `"}}}}</script></html>`)
	got, err := extractHistoryStatement(history)
	if err != nil || got != stmt {
		t.Fatalf("history %q %v", got, err)
	}
	turner := []byte(`<html><script data-drupal-selector="drupal-settings-json">{"top2":{"softwareStatement":"` + stmt + `"}}</script></html>`)
	got, err = extractTurnerStatement(turner)
	if err != nil || got != stmt {
		t.Fatalf("turner %q %v", got, err)
	}
	if _, err := extractHistoryStatement([]byte("<html>no statement</html>")); err == nil {
		t.Fatal("expected a missing history statement")
	}
}

func TestAuthnExpiryFormats(t *testing.T) {
	want := time.Date(2027, 1, 20, 15, 4, 5, 0, time.UTC)
	samples := []string{
		`<simpleTokenExpires>2027/01/20 15:04:05 GMT</simpleTokenExpires>`,
		`<simpleTokenExpires>2027/01/20 15:04:05_GMT</simpleTokenExpires>`,
		`<ns2:simpleTokenExpires format="gmt">2027/01/20 15:04:05 GMT</ns2:simpleTokenExpires>`,
		`<simpleTokenExpires><![CDATA[2027/01/20 15:04:05 GMT]]></simpleTokenExpires>`,
		`<authnToken>&lt;simpleTokenExpires&gt;2027/01/20 15:04:05 GMT&lt;/simpleTokenExpires&gt;</authnToken>`,
		`<simpleTokenExpires>2027-01-20T15:04:05Z</simpleTokenExpires>`,
	}
	for _, sample := range samples {
		got := tokenExpiry(sample)
		if !got.Equal(want) {
			t.Fatalf("expiry %s from %s", got, sample)
		}
	}
	if got := tokenExpiry(authnFixture("2000000000000")); got.Year() != 2033 {
		t.Fatalf("millisecond expiry %s", got)
	}
	if got := tokenExpiry(`<simpleTokenExpires>1790000000</simpleTokenExpires>`); got.Unix() != 1790000000 {
		t.Fatalf("second expiry %s", got)
	}
	if !tokenExpiry(`<simpleTokenTTL>43200</simpleTokenTTL>`).IsZero() || !tokenExpiry(`<simpleTokenExpires>43200</simpleTokenExpires>`).IsZero() {
		t.Fatal("a 12 hour duration must not become the authentication expiry")
	}
	if !tokenExpiry(`<simpleTokenExpires>not-a-time</simpleTokenExpires>`).IsZero() {
		t.Fatal("unreadable expiry should stay empty")
	}
	check := authnCheckExpiry([]byte(`<?xml version="1.0"?><authentication><expires>2000000000000</expires></authentication>`))
	if check.Year() != 2033 {
		t.Fatalf("check expiry %s", check)
	}
}

func TestAuthnTokenShape(t *testing.T) {
	raw := authnFixture("2000000000000")
	token, exp, err := normalizeAuthnToken(raw)
	if err != nil || !strings.Contains(token, "simpleTokenAuthenticationGuid") || exp.IsZero() {
		t.Fatalf("%q %s %v", token, exp, err)
	}
	if _, _, err := normalizeAuthnToken(authnFixture("1500000000000")); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired err %v", err)
	}
	if _, _, err := normalizeAuthnToken("<authnToken><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>"); err == nil || !strings.Contains(err.Error(), "no expiry") {
		t.Fatalf("missing expiry %v", err)
	}
}

func authnFixture(exp string) string {
	return "<authnToken><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid><simpleTokenExpires>" + exp + "</simpleTokenExpires></authnToken>"
}
