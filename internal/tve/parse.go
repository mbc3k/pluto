package tve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mbc3k/pluto/internal/source"
)

// ParseAESchedule reads one day of an A+E schedule document. Times are
// millisecond Unix timestamps. The series title wins over the episode title.
func ParseAESchedule(channelID string, body []byte) ([]source.Program, error) {
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	var out []source.Program
	seen := map[string]struct{}{}
	for _, row := range rows {
		id, _ := row["timeslotId"].(string)
		if id == "" {
			if n, ok := row["id"].(float64); ok {
				id = strings.TrimSpace(strings.TrimRight(strings.TrimRight(sprintfFloat(n), "0"), "."))
			}
		}
		if id != "" {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		startMs, ok1 := asInt(row["startTime"])
		endMs, ok2 := asInt(row["endTime"])
		if !ok1 || !ok2 || endMs <= startMs {
			continue
		}
		series := nestedString(row, "series", "title")
		program := nestedString(row, "program", "title")
		parent := nestedString(row, "parent", "title")
		title := series
		if title == "" {
			title = program
		}
		if title == "" {
			title = parent
		}
		if title == "" {
			continue
		}
		p := source.Program{
			Title: title,
			Start: time.UnixMilli(startMs).UTC(),
			Stop:  time.UnixMilli(endMs).UTC(),
			Desc:  firstNonEmpty(nestedString(row, "program", "description"), nestedString(row, "program", "shortDescription")),
		}
		if program != "" && program != title {
			p.Subtitle = program
		}
		out = append(out, p)
	}
	sortPrograms(out)
	return out, nil
}

// ParseAENowPlaying reads the live "now playing" GraphQL payload.
func ParseAENowPlaying(body []byte) (source.Program, bool) {
	var payload struct {
		Data struct {
			NowPlaying map[string]any `json:"nowPlaying"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data.NowPlaying == nil {
		return source.Program{}, false
	}
	row := payload.Data.NowPlaying
	start, ok1 := asInt(row["startTime"])
	end, ok2 := asInt(row["endTime"])
	if !ok1 || !ok2 || end <= start {
		return source.Program{}, false
	}
	program, _ := row["program"].(map[string]any)
	if program == nil {
		return source.Program{}, false
	}
	kind := nestedString(program, "__typename")
	series := nestedString(program, "series", "title")
	name := nestedString(program, "title")
	title := name
	if kind == "Episode" && series != "" {
		title = series
	}
	if title == "" {
		return source.Program{}, false
	}
	p := source.Program{
		Title: title,
		Desc:  nestedString(program, "description"),
		Start: time.Unix(start, 0).UTC(),
		Stop:  time.Unix(end, 0).UTC(),
	}
	if kind == "Episode" && name != "" && name != title {
		p.Subtitle = name
	}
	return p, true
}

// ParseWarnerWidget reads the Turner schedule widget. The live page is HTML
// with a __NEXT_DATA__ blob; a bare groupedSchedule JSON object is accepted
// too. East feeds use the absolute "gmt" instant. West feeds use "pacific",
// interpreted in America/Los_Angeles.
func ParseWarnerWidget(channelID string, body []byte) ([]source.Program, error) {
	groupedBody, err := warnerScheduleJSON(body)
	if err != nil {
		return nil, err
	}
	var grouped map[string]json.RawMessage
	if err := json.Unmarshal(groupedBody, &grouped); err != nil {
		return nil, err
	}
	west := strings.HasSuffix(channelID, "-west")
	field := "gmt"
	loc := time.UTC
	if west {
		field = "pacific"
		if pacific, err := time.LoadLocation("America/Los_Angeles"); err == nil {
			loc = pacific
		}
	}
	seen := map[string]struct{}{}
	var out []source.Program
	for _, raw := range grouped {
		var entries []map[string]any
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		for _, entry := range entries {
			when, _ := entry[field].(string)
			start, ok := parseWidgetTime(when, loc)
			if !ok {
				continue
			}
			minutes, ok := asInt(entry["slotLength"])
			if !ok || minutes <= 0 {
				continue
			}
			show, _ := entry["show"].(map[string]any)
			episode, _ := show["episode"].(map[string]any)
			title := strings.TrimSpace(asString(show["programTitle"]))
			if title == "" {
				continue
			}
			sub := strings.TrimSpace(asString(episode["episodeTitle"]))
			if sub == title {
				sub = ""
			}
			key := start.UTC().Format(time.RFC3339) + "|" + title + "|" + sub
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			desc := strings.TrimSpace(asString(episode["description"]))
			if desc == "" {
				desc = strings.TrimSpace(asString(show["seriesDesc"]))
			}
			out = append(out, source.Program{
				Title:    title,
				Subtitle: sub,
				Desc:     desc,
				Start:    start,
				Stop:     start.Add(time.Duration(minutes) * time.Minute),
			})
		}
	}
	sortPrograms(out)
	return out, nil
}

func parseWidgetTime(value string, loc *time.Location) (time.Time, bool) {
	parts := strings.Fields(value)
	if len(parts) != 6 {
		return time.Time{}, false
	}
	naive := strings.Join(append(parts[:4], parts[5]), " ")
	t, err := time.ParseInLocation("Mon Jan 02 15:04:05 2006", naive, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func warnerScheduleJSON(body []byte) ([]byte, error) {
	trim := bytes.TrimSpace(body)
	if len(trim) > 0 && (trim[0] == '{' || trim[0] == '[') {
		return trim, nil
	}
	raw, ok := scriptBody(body, "__NEXT_DATA__")
	if !ok {
		return nil, fmt.Errorf("schedule widget has no groupedSchedule")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("schedule widget was not json")
	}
	props, _ := doc["props"].(map[string]any)
	page, _ := props["pageProps"].(map[string]any)
	grouped, ok := page["groupedSchedule"]
	if !ok || grouped == nil {
		return nil, fmt.Errorf("schedule widget has no groupedSchedule")
	}
	out, err := json.Marshal(grouped)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseTCMSchedule reads TCM's public listings document. scheduledDuration
// is seconds and may arrive as a string. The title is show.title.en_US.full.
func ParseTCMSchedule(body []byte) ([]source.Program, error) {
	var payload struct {
		Data struct {
			Entries []map[string]any `json:"getScheduleEntries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var out []source.Program
	for _, entry := range payload.Data.Entries {
		ts := strings.TrimSpace(asString(entry["scheduledTimestamp"]))
		start, err := time.Parse(time.RFC3339, strings.Replace(ts, ".000Z", "Z", 1))
		if err != nil {
			if start, err = time.Parse(time.RFC3339Nano, ts); err != nil {
				continue
			}
		}
		seconds, ok := asInt(entry["scheduledDuration"])
		if !ok {
			if n, err := json.Number(strings.TrimSpace(asString(entry["scheduledDuration"]))).Int64(); err == nil {
				seconds, ok = n, true
			}
		}
		if !ok || seconds <= 0 {
			continue
		}
		show, _ := entry["show"].(map[string]any)
		title := nestedString(show, "title", "en_US", "full")
		if title == "" {
			continue
		}
		summary, _ := entry["summary"].(map[string]any)
		desc := strings.TrimSpace(asString(summary["full"]))
		if desc == "" {
			desc = strings.TrimSpace(asString(summary["short"]))
		}
		out = append(out, source.Program{
			Title: title,
			Desc:  desc,
			Start: start.UTC(),
			Stop:  start.Add(time.Duration(seconds) * time.Second),
		})
	}
	sortPrograms(out)
	return out, nil
}

// ParseAMCSchedule reads one AMC Networks schedule page. The listings node
// is embedded in the HTML. Times are America/New_York wall clocks.
func ParseAMCSchedule(body []byte) ([]source.Program, error) {
	node, err := amcListingsNode(body)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*60*60)
	}
	var out []source.Program
	seen := map[string]struct{}{}
	for _, item := range findScheduleItems(node) {
		text, _ := item["text"].(map[string]any)
		if text == nil {
			continue
		}
		start, ok1 := amcClock(asString(text["startDate"]), asString(text["startTime"]), loc)
		stop, ok2 := amcClock(asString(text["endDate"]), asString(text["endTime"]), loc)
		if !ok1 || !ok2 || !stop.After(start) {
			continue
		}
		title := strings.TrimSpace(asString(text["showTitle"]))
		if title == "" {
			continue
		}
		sub := strings.TrimSpace(asString(text["episodeTitle"]))
		if sub == title {
			sub = ""
		}
		key := start.UTC().Format(time.RFC3339) + "|" + title + "|" + sub
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, source.Program{
			Title:    title,
			Subtitle: sub,
			Desc:     strings.TrimSpace(asString(text["description"])),
			Start:    start.UTC(),
			Stop:     stop.UTC(),
		})
	}
	sortPrograms(out)
	return out, nil
}

func amcListingsNode(body []byte) (any, error) {
	const marker = `{"type":"tv-listings"`
	if i := bytes.Index(body, []byte(marker)); i >= 0 {
		var node any
		if err := json.Unmarshal(body[i:], &node); err != nil {
			dec := json.NewDecoder(bytes.NewReader(body[i:]))
			if err2 := dec.Decode(&node); err2 != nil {
				return nil, fmt.Errorf("amc schedule listings were not json")
			}
		}
		return node, nil
	}
	trim := bytes.TrimSpace(body)
	if len(trim) > 0 && trim[0] == '{' {
		var node any
		if err := json.Unmarshal(trim, &node); err != nil {
			return nil, err
		}
		return node, nil
	}
	return nil, fmt.Errorf("amc schedule page has no tv-listings")
}

func findScheduleItems(node any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if items, ok := n["scheduleItems"].([]any); ok {
				for _, item := range items {
					obj, _ := item.(map[string]any)
					if obj == nil {
						continue
					}
					if props, ok := obj["properties"].(map[string]any); ok {
						out = append(out, props)
					}
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(node)
	return out
}

func amcClock(date, clock string, loc *time.Location) (time.Time, bool) {
	date = strings.TrimSpace(date)
	clock = strings.ToUpper(strings.TrimSpace(clock))
	if date == "" || clock == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 3:04PM", "2006-01-02 3PM", "2006-01-02 15:04"} {
		t, err := time.ParseInLocation(layout, date+" "+clock, loc)
		if err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseFoxNewsSchedule reads the Fox News or Fox Business schedule feed.
func ParseFoxNewsSchedule(body []byte) ([]source.Program, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var out []source.Program
	for _, day := range asList(payload["day"]) {
		obj, _ := day.(map[string]any)
		if obj == nil {
			continue
		}
		for _, show := range asList(obj["show"]) {
			row, _ := show.(map[string]any)
			if row == nil {
				continue
			}
			start, ok1 := foxTime(row["start-utc"])
			if !ok1 {
				start, ok1 = foxTime(row["start"])
			}
			stop, ok2 := foxTime(row["end-utc"])
			if !ok2 {
				stop, ok2 = foxTime(row["end"])
			}
			if !ok1 || !ok2 || !stop.After(start) {
				continue
			}
			title := asText(row["title"])
			if title == "" {
				continue
			}
			desc := asText(row["long-description"])
			if desc == "" {
				desc = asText(row["description"])
			}
			if desc == "" {
				desc = asText(row["short-description"])
			}
			out = append(out, source.Program{
				Title: title,
				Desc:  desc,
				Start: start,
				Stop:  stop,
			})
		}
	}
	sortPrograms(out)
	return out, nil
}

// FoxSportsPage is one page of Fox sports listings, keyed by our channel id.
type FoxSportsPage struct {
	ByChannel map[string][]source.Program
	Next      string
}

// ParseFoxSportsPage reads one Fox sports listings response. callSign maps
// an upstream call sign to a channel id.
func ParseFoxSportsPage(body []byte, callSign map[string]string) (FoxSportsPage, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return FoxSportsPage{}, err
	}
	data, _ := payload["data"].(map[string]any)
	listings, _ := data["listings"].(map[string]any)
	page := FoxSportsPage{ByChannel: map[string][]source.Program{}}
	for _, item := range asList(listings["items"]) {
		row, _ := item.(map[string]any)
		if row == nil {
			continue
		}
		id := callSign[strings.ToUpper(asText(row["call_sign"]))]
		if id == "" {
			continue
		}
		start, ok1 := foxTime(row["start_time"])
		stop, ok2 := foxTime(row["end_time"])
		if !ok1 || !ok2 || !stop.After(start) {
			continue
		}
		title := asText(row["title"])
		if title == "" {
			title = asText(row["series_title"])
		}
		if title == "" {
			continue
		}
		page.ByChannel[id] = append(page.ByChannel[id], source.Program{
			Title: title,
			Desc:  asText(row["description"]),
			Start: start,
			Stop:  stop,
		})
	}
	scroll, _ := data["scroll"].(map[string]any)
	page.Next = strings.TrimSpace(asString(scroll["next"]))
	return page, nil
}

// ParseDiscoveryListings reads a Discovery channel route. LISTING videos are
// the guide. description is the series title and name is the episode title.
func ParseDiscoveryListings(body []byte) ([]source.Program, error) {
	var payload struct {
		Included []struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"included"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var out []source.Program
	seen := map[string]struct{}{}
	for _, item := range payload.Included {
		if item.Type != "video" {
			continue
		}
		if asString(item.Attributes["videoType"]) != "LISTING" {
			continue
		}
		start, ok1 := foxTime(item.Attributes["scheduleStart"])
		stop, ok2 := foxTime(item.Attributes["scheduleEnd"])
		if !ok1 || !ok2 || !stop.After(start) {
			continue
		}
		title := strings.TrimSpace(asString(item.Attributes["description"]))
		if title == "" {
			title = strings.TrimSpace(asString(item.Attributes["name"]))
		}
		if title == "" {
			continue
		}
		sub := strings.TrimSpace(asString(item.Attributes["name"]))
		if sub == title {
			sub = ""
		}
		key := start.UTC().Format(time.RFC3339) + "|" + title + "|" + sub
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, source.Program{
			Title:    title,
			Subtitle: sub,
			Desc:     title,
			Start:    start,
			Stop:     stop,
		})
	}
	sortPrograms(out)
	return out, nil
}

func foxTime(v any) (time.Time, bool) {
	s := strings.TrimSpace(asText(v))
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func asList(v any) []any {
	switch n := v.(type) {
	case nil:
		return nil
	case []any:
		return n
	default:
		return []any{n}
	}
}

func asText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, key := range []string{"#text", "value", "name", "full"} {
			if s := asText(t[key]); s != "" {
				return s
			}
		}
	case []any:
		for _, item := range t {
			if s := asText(item); s != "" {
				return s
			}
		}
	}
	return ""
}

func scriptBody(doc []byte, marker string) ([]byte, bool) {
	i := bytes.Index(doc, []byte(marker))
	if i < 0 {
		return nil, false
	}
	gt := bytes.Index(doc[i:], []byte(">"))
	if gt < 0 {
		return nil, false
	}
	start := i + gt + 1
	end := bytes.Index(doc[start:], []byte("</script>"))
	if end < 0 {
		return nil, false
	}
	return bytes.TrimSpace(doc[start : start+end]), true
}

func sortPrograms(in []source.Program) {
	for i := 1; i < len(in); i++ {
		j := i
		for j > 0 && in[j].Start.Before(in[j-1].Start) {
			in[j], in[j-1] = in[j-1], in[j]
			j--
		}
	}
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func nestedString(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[k]
	}
	return strings.TrimSpace(asString(cur))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sprintfFloat(n float64) string {
	return strings.TrimSpace(strings.ReplaceAll(jsonNumber(n), "\n", ""))
}

func jsonNumber(n float64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
