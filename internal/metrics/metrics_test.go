package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestExpositionIncludesZerosAndIncrements(t *testing.T) {
	var buf bytes.Buffer
	New().Write(&buf, Gauges{})
	body := buf.String()
	for _, want := range []string{
		"pluto_ready 0\n",
		"pluto_auth_ready 0\n",
		"pluto_channels 0\n",
		"pluto_tuners 0\n",
		"pluto_guide_hours 0\n",
		"pluto_last_refresh_timestamp_seconds 0\n",
		"pluto_refresh_total{result=\"ok\"} 0\n",
		"pluto_refresh_total{result=\"error\"} 0\n",
		"pluto_http_responses_total{handler=\"playlist\",code=\"200\"} 0\n",
		"pluto_http_responses_total{handler=\"auth\",code=\"401\"} 0\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}

	rec := New()
	rec.Refresh(true)
	rec.Refresh(false)
	rec.Hit("playlist", 200)
	rec.Hit("playlist", 503)
	rec.Hit("playlist", 400)
	rec.Hit("epg", 200)
	rec.Hit("auth", 401)
	rec.Hit("auth", 500)
	rec.Hit("ignored", 200)
	var nilRec *Recorder
	nilRec.Refresh(true)
	nilRec.Hit("playlist", 200)

	buf.Reset()
	rec.Write(&buf, Gauges{Ready: true, AuthReady: true, Channels: 3, Tuners: 2, GuideHours: 36, LastRefresh: 10})
	body = buf.String()
	for _, want := range []string{
		"pluto_ready 1\n",
		"pluto_auth_ready 1\n",
		"pluto_channels 3\n",
		"pluto_tuners 2\n",
		"pluto_guide_hours 36\n",
		"pluto_last_refresh_timestamp_seconds 10\n",
		"pluto_refresh_total{result=\"ok\"} 1\n",
		"pluto_refresh_total{result=\"error\"} 1\n",
		"pluto_http_responses_total{handler=\"playlist\",code=\"200\"} 1\n",
		"pluto_http_responses_total{handler=\"playlist\",code=\"503\"} 1\n",
		"pluto_http_responses_total{handler=\"playlist\",code=\"other\"} 1\n",
		"pluto_http_responses_total{handler=\"epg\",code=\"200\"} 1\n",
		"pluto_http_responses_total{handler=\"epg\",code=\"503\"} 0\n",
		"pluto_http_responses_total{handler=\"auth\",code=\"401\"} 1\n",
		"pluto_http_responses_total{handler=\"auth\",code=\"other\"} 1\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
}
