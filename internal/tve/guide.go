package tve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mbc3k/pluto/internal/source"
)

const (
	warnerWidgetURL = "https://tnets-dvs-schedule.wme-digital.com/"
	tcmGuideURL     = "https://wme-gep-graphql-prod.wme-digital.com/graphql"
	foxSportsURL    = "https://api.fox.com/fs/product/curated/v1/sporting/keystone/detail/by_filters"
	// foxSportsKey is the client key the public Fox sports listings request
	// sends. It is not a DirecTV credential.
	foxSportsKey = "cf289e299efdfa39fb6316f259d1de93"
	discoveryAPI = "https://us1-prod-direct.watch.hgtv.com"
	tcmEastHash  = "93c7a879d46f6a788dcdba08f79b670a09d2fa09eadb5e04506d26768489e5c7"
	tcmWestHash  = "0d063c0ae8c94dc525fe9eac45ffff42a34acaa8c3b9fa048dbf8e562dc968fc"
)

var foxNewsURL = map[string]string{
	"fox_news":     "https://apps.foxnews.com/schedule_new/feed/fox-news.jn",
	"fox_business": "https://apps.foxnews.com/schedule_new/feed/fox-business.jn",
}

var foxCallSign = map[string]string{
	"FS1":         "fox_sports_fs1",
	"FS1-DIGITAL": "fox_sports_fs1",
	"FS2":         "fox_sports_fs2",
	"FS2-DIGITAL": "fox_sports_fs2",
	"FOX":         "fox_sports_fox",
	"FOX-DIGITAL": "fox_sports_fox",
	"BTN":         "fox_sports_btn",
	"FOXDEP":      "fox_deportes",
	"FSP":         "fox_soccer_plus",
}

var amcScheduleURL = map[string]string{
	"amc":  "https://www.amc.com/schedule/",
	"bbca": "https://www.bbcamerica.com/schedule/",
	"ifc":  "https://www.ifc.com/schedule/",
	"wetv": "https://www.wetv.com/schedule/",
}

var amcOrigin = map[string]string{
	"amc":  "https://www.amc.com/",
	"bbca": "https://www.bbcamerica.com/",
	"ifc":  "https://www.ifc.com/",
	"wetv": "https://www.wetv.com/",
}

var discoverySlug = map[string]string{
	"tlc":           "tlc",
	"food-network":  "food-network",
	"discovery":     "discovery",
	"hgtv":          "hgtv",
	"id":            "investigation-discovery",
	"animal-planet": "animal-planet",
	"own":           "own",
	"travel":        "travel-channel",
	"cooking":       "cooking-channel",
	"magnolia":      "magnolia-network-preview-atve-us",
}

func (s *Service) fillGuide(ctx context.Context, id string, channels []source.Channel) error {
	var err error
	switch id {
	case SourceAE:
		err = s.fillAE(ctx, channels)
	case SourceAMC:
		err = s.fillAMC(ctx, channels)
	case SourceWarner:
		err = s.fillWarner(ctx, channels)
	case SourceFox:
		err = s.fillFox(ctx, channels)
	case SourceDiscovery:
		err = s.fillDiscovery(ctx, channels)
	}
	return finishFill(channels, err)
}

func finishFill(channels []source.Channel, err error) error {
	if err != nil && programmeCount(channels) == 0 {
		return err
	}
	return nil
}

func programmeCount(channels []source.Channel) int {
	n := 0
	for _, ch := range channels {
		n += len(ch.Programs)
	}
	return n
}

func (s *Service) fillAMC(ctx context.Context, channels []source.Channel) error {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc)
	var first error
	for i := range channels {
		page := amcScheduleURL[channels[i].ID]
		if page == "" {
			continue
		}
		var programs []source.Program
		for day := 0; day < 2; day++ {
			d := today.AddDate(0, 0, day)
			u, err := url.Parse(page)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			q := u.Query()
			q.Set("tz", "ET")
			q.Set("from", d.Format("2006-01-02"))
			u.RawQuery = q.Encode()
			body, err := s.getGuide(ctx, SourceAMC, u.String(), map[string]string{
				"Accept":  "text/html",
				"Referer": amcOrigin[channels[i].ID],
			})
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			part, err := ParseAMCSchedule(body.Body)
			if err != nil {
				err = s.noteNonJSON(SourceAMC, body, err)
				if first == nil {
					first = err
				}
				continue
			}
			programs = append(programs, part...)
		}
		if len(programs) > 0 {
			sortPrograms(programs)
			channels[i].Programs = programs
		}
	}
	return first
}

func (s *Service) fillFox(ctx context.Context, channels []source.Channel) error {
	var first error
	for i := range channels {
		rawURL := foxNewsURL[channels[i].ID]
		if rawURL == "" {
			continue
		}
		body, err := s.getGuide(ctx, SourceFox, rawURL, map[string]string{"Accept": "application/json,*/*"})
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		programs, err := ParseFoxNewsSchedule(body.Body)
		if err != nil {
			err = s.noteNonJSON(SourceFox, body, err)
			if first == nil {
				first = err
			}
			continue
		}
		if len(programs) > 0 {
			channels[i].Programs = programs
		}
	}
	sports, err := s.foxSports(ctx)
	if err != nil && first == nil {
		first = err
	}
	for i := range channels {
		programs := sports[channels[i].ID]
		if len(programs) > 0 {
			sortPrograms(programs)
			channels[i].Programs = programs
		}
	}
	return first
}

func (s *Service) foxSports(ctx context.Context) (map[string][]source.Program, error) {
	q := url.Values{
		"callsign":   {"FS1,FS1-DIGITAL,FS2,FS2-DIGITAL,FOX,FOX-DIGITAL,BTN,FOXDEP,FSP"},
		"start_date": {fmt.Sprint(time.Now().Add(-2 * time.Hour).Unix())},
		"end_date":   {fmt.Sprint(time.Now().Add(26 * time.Hour).Unix())},
		"size":       {"50"},
		"video_type": {"listing"},
	}
	next := foxSportsURL + "?" + q.Encode()
	out := map[string][]source.Program{}
	var first error
	for hop := 0; hop < 4 && next != ""; hop++ {
		body, err := s.getGuide(ctx, SourceFox, next, map[string]string{
			"Accept":       "application/json",
			"x-fox-apikey": foxSportsKey,
			"content-type": "application/json",
		})
		if err != nil {
			return out, err
		}
		page, err := ParseFoxSportsPage(body.Body, foxCallSign)
		if err != nil {
			return out, s.noteNonJSON(SourceFox, body, err)
		}
		for id, programs := range page.ByChannel {
			out[id] = append(out[id], programs...)
		}
		next = foxSportsNext(page.Next)
	}
	return out, first
}

func foxSportsNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return ""
	}
	if strings.HasPrefix(next, "/product/") {
		return "https://api.fox.com/fs" + next
	}
	if strings.HasPrefix(next, "http") {
		return next
	}
	return ""
}

func (s *Service) fillDiscovery(ctx context.Context, channels []source.Channel) error {
	token, err := s.discoveryToken(ctx)
	if err != nil {
		return err
	}
	var first error
	headers := map[string]string{
		"Accept":         "application/json",
		"Authorization":  "Bearer " + token,
		"Origin":         "https://watch.hgtv.com",
		"Referer":        "https://watch.hgtv.com/",
		"x-disco-client": "WEB:UNKNOWN:hgtv:27.43.0",
		"x-disco-params": "realm=go,siteLookupKey=hgtv",
	}
	for i := range channels {
		slug := discoverySlug[channels[i].ID]
		if slug == "" {
			continue
		}
		rawURL := discoveryAPI + "/cms/routes/channel/" + url.PathEscape(slug) + "?include=default&decorators=viewingHistory,isFavorite,playbackAllowed"
		body, err := s.getGuide(ctx, SourceDiscovery, rawURL, headers)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		programs, err := ParseDiscoveryListings(body.Body)
		if err != nil {
			err = s.noteNonJSON(SourceDiscovery, body, err)
			if first == nil {
				first = err
			}
			continue
		}
		if len(programs) > 0 {
			channels[i].Programs = programs
		}
	}
	return first
}

func (s *Service) discoveryToken(ctx context.Context) (string, error) {
	device, err := randomHex(16)
	if err != nil {
		return "", err
	}
	rawURL := discoveryAPI + "/token?" + url.Values{
		"realm":      {"go"},
		"deviceId":   {device},
		"shortlived": {"true"},
	}.Encode()
	body, err := s.getGuide(ctx, SourceDiscovery, rawURL, map[string]string{
		"Accept":         "application/json",
		"x-disco-client": "WEB:10:WEB_AUTH:0.37.1",
		"x-disco-params": "realm=go,siteLookupKey=hgtv",
		"x-device-info":  "hgtv/4.5.0 (desktop/desktop; Windows/NT 10.0; " + device + "/6ed92d9d8e3518b46e90)",
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Data struct {
			Attributes struct {
				Token string `json:"token"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body.Body, &payload); err != nil || payload.Data.Attributes.Token == "" {
		return "", s.noteNonJSON(SourceDiscovery, body, fmt.Errorf("discovery token was missing"))
	}
	return payload.Data.Attributes.Token, nil
}

func (s *Service) tcmGuide(ctx context.Context, channelID string) ([]source.Program, error) {
	hash := tcmEastHash
	feed := "tcm"
	if channelID == "tcm-west" {
		hash = tcmWestHash
		feed = "tcm-west"
	}
	start := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	q := url.Values{
		"operationName": {"getListings"},
		"variables":     {fmt.Sprintf(`{"feed":["%s"],"brand":"tcm","startDate":"%s","count":40}`, feed, start)},
		"extensions":    {fmt.Sprintf(`{"persistedQuery":{"version":1,"sha256Hash":"%s"}}`, hash)},
	}
	body, err := s.getGuide(ctx, SourceWarner, tcmGuideURL+"?"+q.Encode(), map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	programs, err := ParseTCMSchedule(body.Body)
	if err != nil {
		return nil, s.noteNonJSON(SourceWarner, body, err)
	}
	return programs, nil
}
