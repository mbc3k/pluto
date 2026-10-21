// Package source is the guide/playlist shape shared by Pluto's TV Everywhere
// networks. Pluto's own tuner playlists stay in internal/playlist.
package source

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// Program is one guide entry on a channel.
type Program struct {
	Title    string
	Subtitle string
	Desc     string
	Category string
	Start    time.Time
	Stop     time.Time
}

// Channel is one linear stream and the guide rows that belong to it.
// StreamURL is either an absolute upstream URL or a path beginning with "/"
// that the server prefixes with its public origin when it serves the playlist.
type Channel struct {
	ID        string
	Name      string
	Logo      string
	Category  string
	StreamURL string
	Programs  []Program
}

// Feed is one custom-channel source: its own playlist and its own guide.
type Feed struct {
	ID       string
	Name     string
	Channels []Channel
	Updated  time.Time
}

// M3U renders a playlist. start is the first tvg-chno. base is used only
// for stream paths that are still local.
func M3U(feed Feed, start int, base string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := range feed.Channels {
		ch := &feed.Channels[i]
		name := oneLine(ch.Name)
		id := oneLine(ch.ID)
		stream := ch.StreamURL
		if strings.HasPrefix(stream, "/") {
			stream = strings.TrimRight(base, "/") + stream
		}
		group := oneLine(ch.Category)
		if group == "" {
			group = oneLine(feed.Name)
		}
		fmt.Fprintf(&b,
			"#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q tvg-chno=%d channel-id=%q group-title=%q,%s\n%s\n",
			id, name, oneLine(ch.Logo), start+i, id, group, name, stream,
		)
	}
	return b.String()
}

// XMLTV renders a guide document. Channels with no programmes are still listed.
func XMLTV(feed Feed) ([]byte, error) {
	doc := tv{
		GeneratorInfoName: "pluto-tve",
		Channels:          make([]xChannel, 0, len(feed.Channels)),
	}
	for i := range feed.Channels {
		ch := &feed.Channels[i]
		name := xmlText(ch.Name)
		if name == "" {
			name = xmlText(ch.ID)
		}
		xc := xChannel{ID: xmlText(ch.ID), DisplayName: []langText{{Lang: "en", Value: name}}}
		if logo := xmlText(ch.Logo); logo != "" {
			xc.Icon = &icon{Src: logo}
		}
		doc.Channels = append(doc.Channels, xc)
		for _, p := range ch.Programs {
			if p.Start.IsZero() || !p.Stop.After(p.Start) || xmlText(p.Title) == "" {
				continue
			}
			prog := programme{
				Start:   p.Start.UTC().Format(xmltvTime),
				Stop:    p.Stop.UTC().Format(xmltvTime),
				Channel: xmlText(ch.ID),
				Title:   []langText{{Lang: "en", Value: xmlText(p.Title)}},
			}
			if sub := xmlText(p.Subtitle); sub != "" {
				prog.SubTitle = []langText{{Lang: "en", Value: sub}}
			}
			if desc := xmlText(p.Desc); desc != "" {
				prog.Desc = []langText{{Lang: "en", Value: desc}}
			}
			if cat := xmlText(p.Category); cat != "" {
				prog.Category = []langText{{Lang: "en", Value: cat}}
			} else if cat := xmlText(ch.Category); cat != "" {
				prog.Category = []langText{{Lang: "en", Value: cat}}
			}
			doc.Programmes = append(doc.Programmes, prog)
		}
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal XMLTV: %w", err)
	}
	var buf []byte
	buf = append(buf, xml.Header...)
	buf = append(buf, out...)
	buf = append(buf, '\n')
	return buf, nil
}

func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func xmlText(s string) string {
	return strings.TrimSpace(oneLine(s))
}

const xmltvTime = "20060102150405 +0000"

type tv struct {
	XMLName           xml.Name    `xml:"tv"`
	GeneratorInfoName string      `xml:"generator-info-name,attr"`
	Channels          []xChannel  `xml:"channel"`
	Programmes        []programme `xml:"programme"`
}

type xChannel struct {
	ID          string     `xml:"id,attr"`
	DisplayName []langText `xml:"display-name"`
	Icon        *icon      `xml:"icon,omitempty"`
}

type programme struct {
	Start    string     `xml:"start,attr"`
	Stop     string     `xml:"stop,attr"`
	Channel  string     `xml:"channel,attr"`
	Title    []langText `xml:"title"`
	SubTitle []langText `xml:"sub-title,omitempty"`
	Desc     []langText `xml:"desc,omitempty"`
	Category []langText `xml:"category,omitempty"`
}

type langText struct {
	Lang  string `xml:"lang,attr"`
	Value string `xml:",chardata"`
}

type icon struct {
	Src string `xml:"src,attr"`
}
