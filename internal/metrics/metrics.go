// Package metrics exposes a small Prometheus text-format snapshot.
// It has no client-library dependency. Counters move when something
// happens; gauges are filled in by the caller at scrape time.
package metrics

import (
	"fmt"
	"io"
	"sync/atomic"
)

// Gauges are point-in-time values the server can read without storing them.
type Gauges struct {
	Ready       bool
	AuthReady   bool
	Channels    int
	Tuners      int
	GuideHours  int
	LastRefresh int64 // unix seconds; 0 if the guide has never loaded
}

// Recorder counts refresh attempts and HTTP outcomes. A nil *Recorder is safe
// to call: the process can run with metrics unwired.
type Recorder struct {
	refreshOK     atomic.Int64
	refreshErr    atomic.Int64
	playlist200   atomic.Int64
	playlist503   atomic.Int64
	playlistOther atomic.Int64
	epg200        atomic.Int64
	epg503        atomic.Int64
	epgOther      atomic.Int64
	auth401       atomic.Int64
	authOther     atomic.Int64
}

// New returns an empty recorder. Every series is emitted, including zeros.
func New() *Recorder { return &Recorder{} }

// Refresh counts one guide refresh that finished while the process was still
// running. ok is false when the attempt returned an error.
func (r *Recorder) Refresh(ok bool) {
	if r == nil {
		return
	}
	if ok {
		r.refreshOK.Add(1)
		return
	}
	r.refreshErr.Add(1)
}

// Hit counts one finished playlist, EPG, or access-gate response.
// handler is "playlist", "epg", or "auth". Unknown codes collapse to "other"
// so the exposition stays a fixed set of series.
func (r *Recorder) Hit(handler string, code int) {
	if r == nil {
		return
	}
	switch handler {
	case "playlist":
		switch code {
		case 200:
			r.playlist200.Add(1)
		case 503:
			r.playlist503.Add(1)
		default:
			r.playlistOther.Add(1)
		}
	case "epg":
		switch code {
		case 200:
			r.epg200.Add(1)
		case 503:
			r.epg503.Add(1)
		default:
			r.epgOther.Add(1)
		}
	case "auth":
		if code == 401 {
			r.auth401.Add(1)
			return
		}
		r.authOther.Add(1)
	}
}

// Write renders the current snapshot in Prometheus text exposition 0.0.4.
func (r *Recorder) Write(w io.Writer, g Gauges) {
	if r == nil {
		r = New()
	}
	fmt.Fprintf(w, "# HELP pluto_ready 1 when the channel cache has been populated.\n")
	fmt.Fprintf(w, "# TYPE pluto_ready gauge\n")
	fmt.Fprintf(w, "pluto_ready %d\n", boolGauge(g.Ready))
	fmt.Fprintf(w, "# HELP pluto_auth_ready 1 after the initial tuner sign-in attempt has finished.\n")
	fmt.Fprintf(w, "# TYPE pluto_auth_ready gauge\n")
	fmt.Fprintf(w, "pluto_auth_ready %d\n", boolGauge(g.AuthReady))
	fmt.Fprintf(w, "# HELP pluto_channels Channels in the current guide snapshot.\n")
	fmt.Fprintf(w, "# TYPE pluto_channels gauge\n")
	fmt.Fprintf(w, "pluto_channels %d\n", g.Channels)
	fmt.Fprintf(w, "# HELP pluto_tuners Configured tuner sessions.\n")
	fmt.Fprintf(w, "# TYPE pluto_tuners gauge\n")
	fmt.Fprintf(w, "pluto_tuners %d\n", g.Tuners)
	fmt.Fprintf(w, "# HELP pluto_guide_hours Guide window length in hours.\n")
	fmt.Fprintf(w, "# TYPE pluto_guide_hours gauge\n")
	fmt.Fprintf(w, "pluto_guide_hours %d\n", g.GuideHours)
	fmt.Fprintf(w, "# HELP pluto_last_refresh_timestamp_seconds Unix time of the last successful guide refresh.\n")
	fmt.Fprintf(w, "# TYPE pluto_last_refresh_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "pluto_last_refresh_timestamp_seconds %d\n", g.LastRefresh)
	fmt.Fprintf(w, "# HELP pluto_refresh_total Guide refresh attempts that finished without cancellation.\n")
	fmt.Fprintf(w, "# TYPE pluto_refresh_total counter\n")
	fmt.Fprintf(w, "pluto_refresh_total{result=\"ok\"} %d\n", r.refreshOK.Load())
	fmt.Fprintf(w, "pluto_refresh_total{result=\"error\"} %d\n", r.refreshErr.Load())
	fmt.Fprintf(w, "# HELP pluto_http_responses_total HTTP responses for playlist, EPG, and the access gate.\n")
	fmt.Fprintf(w, "# TYPE pluto_http_responses_total counter\n")
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"playlist\",code=\"200\"} %d\n", r.playlist200.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"playlist\",code=\"503\"} %d\n", r.playlist503.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"playlist\",code=\"other\"} %d\n", r.playlistOther.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"epg\",code=\"200\"} %d\n", r.epg200.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"epg\",code=\"503\"} %d\n", r.epg503.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"epg\",code=\"other\"} %d\n", r.epgOther.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"auth\",code=\"401\"} %d\n", r.auth401.Load())
	fmt.Fprintf(w, "pluto_http_responses_total{handler=\"auth\",code=\"other\"} %d\n", r.authOther.Load())
}

func boolGauge(v bool) int {
	if v {
		return 1
	}
	return 0
}
