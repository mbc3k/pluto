package epg

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/mbc3k/pluto/internal/pluto"
)

// xmltvTimeFormat is the XMLTV standard date-time format.
const xmltvTimeFormat = "20060102150405 +0000"

// tv is the root XMLTV element.
type tv struct {
	XMLName           xml.Name    `xml:"tv"`
	GeneratorInfoName string      `xml:"generator-info-name,attr"`
	Channels          []xChannel  `xml:"channel"`
	Programmes        []programme `xml:"programme"`
}

type xChannel struct {
	ID          string      `xml:"id,attr"`
	DisplayName []langText  `xml:"display-name"`
	Icon        *icon       `xml:"icon,omitempty"`
}

type programme struct {
	Start   string     `xml:"start,attr"`
	Stop    string     `xml:"stop,attr"`
	Channel string     `xml:"channel,attr"`
	Title   []langText `xml:"title"`
	Desc    []langText `xml:"desc,omitempty"`
	SubTitle []langText `xml:"sub-title,omitempty"`
	EpNum   *epNum     `xml:"episode-num,omitempty"`
	Rating  *rating    `xml:"rating,omitempty"`
}

type langText struct {
	Lang  string `xml:"lang,attr"`
	Value string `xml:",chardata"`
}

type icon struct {
	Src string `xml:"src,attr"`
}

type epNum struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

type rating struct {
	System string    `xml:"system,attr"`
	Value  langText  `xml:"value"`
}

// Generate builds a complete XMLTV document from the given channel list.
// The result is a single EPG shared across all tuners.
func Generate(channels []pluto.Channel) ([]byte, error) {
	doc := tv{
		GeneratorInfoName: "pluto",
	}

	for _, ch := range channels {
		doc.Channels = append(doc.Channels, buildChannel(ch))
		for _, prog := range ch.Timelines {
			doc.Programmes = append(doc.Programmes, buildProgramme(ch.Slug, prog))
		}
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal XMLTV: %w", err)
	}

	result := make([]byte, 0, len(xml.Header)+len(out)+1)
	result = append(result, []byte(xml.Header)...)
	result = append(result, out...)
	result = append(result, '\n')
	return result, nil
}

func buildChannel(ch pluto.Channel) xChannel {
	xch := xChannel{
		ID: ch.Slug,
		DisplayName: []langText{
			{Lang: "en", Value: ch.Name},
		},
	}
	if ch.ColorLogoPNG.Path != "" {
		xch.Icon = &icon{Src: ch.ColorLogoPNG.Path}
	}
	return xch
}

func buildProgramme(channelSlug string, p pluto.Program) programme {
	prog := programme{
		Start:   formatXMLTVTime(p.Start),
		Stop:    formatXMLTVTime(p.Stop),
		Channel: channelSlug,
		Title: []langText{
			{Lang: "en", Value: p.Title},
		},
	}

	// Episode name as sub-title when present and distinct from title.
	if p.Episode.Name != "" && p.Episode.Name != p.Title {
		prog.SubTitle = []langText{
			{Lang: "en", Value: p.Episode.Name},
		}
	}

	if p.Episode.Description != "" {
		prog.Desc = []langText{
			{Lang: "en", Value: p.Episode.Description},
		}
	}

	// Episode numbering in xmltv "onscreen" format: S01E02.
	if p.Episode.Season > 0 && p.Episode.Number > 0 {
		prog.EpNum = &epNum{
			System: "onscreen",
			Value:  fmt.Sprintf("S%02dE%02d", p.Episode.Season, p.Episode.Number),
		}
	}

	if p.Episode.Rating != "" {
		prog.Rating = &rating{
			System: "VCHIP",
			Value:  langText{Lang: "en", Value: p.Episode.Rating},
		}
	}

	return prog
}

func formatXMLTVTime(t time.Time) string {
	return t.UTC().Format(xmltvTimeFormat)
}
