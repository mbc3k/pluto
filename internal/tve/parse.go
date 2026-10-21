package tve

import (
	"encoding/json"
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

// ParseWarnerWidget reads the Turner schedule widget. East feeds use the
// absolute "gmt" instant. West feeds use "pacific", interpreted in
// America/Los_Angeles.
func ParseWarnerWidget(channelID string, body []byte) ([]source.Program, error) {
	var grouped map[string]json.RawMessage
	if err := json.Unmarshal(body, &grouped); err != nil {
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
