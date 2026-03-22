package pluto

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"time"
)

// slugBlocklist matches channel slugs that should be excluded from output.
// These are announcement/policy channels that aren't real streams.
var slugBlocklist = regexp.MustCompile(`^(announcement|privacy-policy)`)

// slugPrefixMap maps slugs that conflict with other providers and need
// a "pluto-" prefix to disambiguate them in Channels DVR.
var slugPrefixMap = map[string]bool{
	"cnn": true, "dabl": true, "heartland": true, "newsy": true, "buzzr": true,
}

const channelsAPI = "https://api.pluto.tv/v2/channels"

// FetchChannels retrieves the full channel list with timeline data from
// the Pluto TV API. It fetches 4 consecutive 6-hour windows to build a
// full 24-hour EPG, merging timeline entries across responses.
func FetchChannels(ctx context.Context, client *RetryClient) ([]Channel, error) {
	now := time.Now().UTC().Truncate(6 * time.Hour)

	// Collect timelines per channel across the 4 windows.
	type channelKey = string
	channelMap := make(map[channelKey]*Channel)
	var channelOrder []string

	for i := 0; i < 4; i++ {
		start := now.Add(time.Duration(i) * 6 * time.Hour)
		stop := start.Add(6 * time.Hour)

		channels, err := fetchWindow(ctx, client, start, stop)
		if err != nil {
			return nil, fmt.Errorf("window %d: %w", i, err)
		}

		for idx := range channels {
			ch := &channels[idx]
			if slugBlocklist.MatchString(ch.Slug) || !ch.IsStitched {
				continue
			}
			if existing, ok := channelMap[ch.ID]; ok {
				existing.Timelines = append(existing.Timelines, ch.Timelines...)
			} else {
				// Normalize slug to avoid DVR conflicts.
				if slugPrefixMap[ch.Slug] {
					ch.Slug = "pluto-" + ch.Slug
				}
				copy := *ch
				channelMap[ch.ID] = &copy
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
	format := "2006-01-02 15:04:05.000+0000"

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
