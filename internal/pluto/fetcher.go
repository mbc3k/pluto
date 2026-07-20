package pluto

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// isBlockedSlug reports channel slugs that should be excluded from output.
// These are announcement/policy channels that aren't real streams.
func isBlockedSlug(slug string) bool {
	return strings.HasPrefix(slug, "announcement") || strings.HasPrefix(slug, "privacy-policy")
}

// slugPrefixMap maps slugs that conflict with other providers and need
// a "pluto-" prefix to disambiguate them in Channels DVR.
var slugPrefixMap = map[string]bool{
	"cnn": true, "dabl": true, "heartland": true, "newsy": true, "buzzr": true,
}

const (
	channelsAPI = "https://api.pluto.tv/v2/channels"
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

	// Collect timelines per channel across the windows, preserving first-seen order.
	channelMap := make(map[string]*Channel)
	var channelOrder []string

	for i, res := range results {
		if res.err != nil {
			return nil, fmt.Errorf("window %d: %w", i, res.err)
		}
		for idx := range res.channels {
			ch := &res.channels[idx]
			if isBlockedSlug(ch.Slug) || !ch.IsStitched {
				continue
			}
			if existing, ok := channelMap[ch.ID]; ok {
				existing.Timelines = append(existing.Timelines, ch.Timelines...)
			} else {
				// Normalize slug to avoid DVR conflicts.
				if slugPrefixMap[ch.Slug] {
					ch.Slug = "pluto-" + ch.Slug
				}
				// Copy so subsequent window iterations can reuse the result slice
				// without aliasing into channelMap.
				cp := *ch
				channelMap[ch.ID] = &cp
				channelOrder = append(channelOrder, ch.ID)
			}
		}
	}

	result := make([]Channel, 0, len(channelOrder))
	for _, id := range channelOrder {
		result = append(result, *channelMap[id])
	}

	// Stable sort by Pluto's own channel number so our numbering is consistent.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Number < result[j].Number
	})

	return result, nil
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

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("channels API returned HTTP %d", resp.StatusCode)
	}

	var channels ChannelResponse
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return nil, fmt.Errorf("decode channels: %w", err)
	}

	return []Channel(channels), nil
}
