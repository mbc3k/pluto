package pluto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// isBlockedSlug reports channel slugs that should be excluded from output.
// These are announcement/policy channels that aren't real streams.
func isBlockedSlug(slug string) bool {
	s := strings.ToLower(slug)
	return strings.HasPrefix(s, "announcement") || strings.HasPrefix(s, "privacy-policy")
}

// slugPrefixMap maps slugs that conflict with other providers and need
// a "pluto-" prefix to disambiguate them in Channels DVR.
var slugPrefixMap = map[string]bool{
	"cnn": true, "dabl": true, "heartland": true, "newsy": true, "buzzr": true,
}

// channelsAPI is the Pluto channel guide. Tests point it at a local server.
var channelsAPI = "https://api.pluto.tv/v2/channels"

const (
	// epgWindows is the number of consecutive 6-hour windows that make a 24h EPG.
	epgWindows = 4
)

// FetchChannels retrieves the full channel list with timeline data from
// the Pluto TV API. It fetches 4 concurrent 6-hour windows to build a
// full 24-hour EPG, merging timeline entries across responses.
func FetchChannels(ctx context.Context, client *RetryClient) ([]Channel, error) {
	now := time.Now().UTC().Truncate(6 * time.Hour)

	type windowResult struct {
		channels []Channel
		err      error
	}
	results := make([]windowResult, epgWindows)

	var wg sync.WaitGroup
	for i := 0; i < epgWindows; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := now.Add(time.Duration(i) * 6 * time.Hour)
			stop := start.Add(6 * time.Hour)
			ch, err := fetchWindow(ctx, client, start, stop)
			results[i] = windowResult{channels: ch, err: err}
		}(i)
	}
	wg.Wait()

	windows := make([][]Channel, 0, epgWindows)
	for i, res := range results {
		if res.err != nil {
			return nil, fmt.Errorf("window %d: %w", i, res.err)
		}
		windows = append(windows, res.channels)
	}
	return mergeChannelWindows(windows)
}

// mergeChannelWindows combines per-window channel payloads into one list.
// Order follows Pluto's channel number, with slug and id as tie-breaks so a
// refresh cannot renumber channels that share a number. Programmes that
// appear in more than one window are kept once.
func mergeChannelWindows(windows [][]Channel) ([]Channel, error) {
	channelMap := make(map[string]*Channel)
	var channelOrder []string

	for _, window := range windows {
		for idx := range window {
			ch := &window[idx]
			if ch.ID == "" || ch.Slug == "" || isBlockedSlug(ch.Slug) {
				continue
			}
			if existing, ok := channelMap[ch.ID]; ok {
				existing.Timelines = append(existing.Timelines, ch.Timelines...)
				continue
			}
			if !ch.IsStitched {
				continue
			}
			cp := *ch
			if slugPrefixMap[strings.ToLower(cp.Slug)] {
				cp.Slug = "pluto-" + cp.Slug
			}
			if cp.Name == "" {
				cp.Name = cp.Slug
			}
			// Copy timelines so later appends cannot alias the decoded window.
			cp.Timelines = append([]Program(nil), ch.Timelines...)
			channelMap[ch.ID] = &cp
			channelOrder = append(channelOrder, ch.ID)
		}
	}

	if len(channelOrder) == 0 {
		return nil, fmt.Errorf("no usable channels")
	}

	result := make([]Channel, 0, len(channelOrder))
	for _, id := range channelOrder {
		ch := *channelMap[id]
		ch.Timelines = dedupeTimelines(ch.Timelines)
		result = append(result, ch)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Number != result[j].Number {
			return result[i].Number < result[j].Number
		}
		if result[i].Slug != result[j].Slug {
			return result[i].Slug < result[j].Slug
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// dedupeTimelines sorts by start time and drops repeats and unusable rows.
func dedupeTimelines(in []Program) []Program {
	if len(in) == 0 {
		return nil
	}
	sort.SliceStable(in, func(i, j int) bool {
		if !in[i].Start.Equal(in[j].Start) {
			return in[i].Start.Before(in[j].Start)
		}
		return in[i].ID < in[j].ID
	})
	out := make([]Program, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, p := range in {
		if p.Start.IsZero() || !p.Stop.After(p.Start) {
			continue
		}
		key := p.ID
		if key == "" {
			key = p.Start.UTC().Format(time.RFC3339Nano) + "|" + p.Stop.UTC().Format(time.RFC3339Nano) + "|" + p.Title
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func fetchWindow(ctx context.Context, client *RetryClient, start, stop time.Time) ([]Channel, error) {
	// Pluto TV's expected timestamp format: "2006-01-02 15:04:05.000+0000"
	const format = "2006-01-02 15:04:05.000+0000"

	params := url.Values{}
	params.Set("start", start.UTC().Format(format))
	params.Set("stop", stop.UTC().Format(format))

	u := channelsAPI + "?" + params.Encode()

	resp, err := client.Get(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		msg := strings.TrimSpace(string(body))
		msg = strings.ReplaceAll(msg, "\n", " ")
		return nil, fmt.Errorf("channels API HTTP %d: %s", resp.StatusCode, msg)
	}

	var channels ChannelResponse
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return nil, fmt.Errorf("decode channels: %w", err)
	}

	return []Channel(channels), nil
}
