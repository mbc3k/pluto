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
	Start    string     `xml:"start,attr"`
	Stop     string     `xml:"stop,attr"`
	Channel  string     `xml:"channel,attr"`
	Title    []langText `xml:"title"`
	Desc     []langText `xml:"desc,omitempty"`
	SubTitle []langText `xml:"sub-title,omitempty"`
	Category []langText `xml:"category,omitempty"`
	EpNum    *epNum     `xml:"episode-num,omitempty"`
	Rating   *rating    `xml:"rating,omitempty"`
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
	nProg := 0
	for i := range channels {
		nProg += len(channels[i].Timelines)
	}

	doc := tv{
		GeneratorInfoName: "pluto",
		Channels:          make([]xChannel, 0, len(channels)),
		Programmes:        make([]programme, 0, nProg),
	}

	for i := range channels {
		ch := &channels[i]
		doc.Channels = append(doc.Channels, buildChannel(ch))
		for j := range ch.Timelines {
			doc.Programmes = append(doc.Programmes, buildProgramme(ch.Slug, ch.Category, &ch.Timelines[j]))
		}
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal XMLTV: %w", err)
	}

	result := make([]byte, 0, len(xml.Header)+len(out)+1)
	result = append(result, xml.Header...)
	result = append(result, out...)
	result = append(result, '\n')
	return result, nil
}

func buildChannel(ch *pluto.Channel) xChannel {
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

// isFilm returns true if the program's series type indicates a movie/film.
func isFilm(p *pluto.Program) bool {
	return p.Episode.Series.Type == "film"
}

// programmeCategory returns the XMLTV category for a program.
// Films get "Movie" so DVR software files them correctly.
// Other programs get the channel's Pluto category (e.g. "Kids", "News")
// plus the episode genre when available.
func programmeCategory(channelCategory string, p *pluto.Program) []langText {
	var cats []langText
	if isFilm(p) {
		cats = append(cats, langText{Lang: "en", Value: "Movie"})
	}
	if p.Episode.Genre != "" {
		cats = append(cats, langText{Lang: "en", Value: p.Episode.Genre})
	}
	if p.Episode.SubGenre != "" && p.Episode.SubGenre != p.Episode.Genre {
		cats = append(cats, langText{Lang: "en", Value: p.Episode.SubGenre})
	}
	if channelCategory != "" && channelCategory != p.Episode.Genre {
		cats = append(cats, langText{Lang: "en", Value: channelCategory})
	}
	return cats
}

func buildProgramme(channelSlug, channelCategory string, p *pluto.Program) programme {
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

	// Category tags for DVR classification (Movie, genre, etc.).
	prog.Category = programmeCategory(channelCategory, p)

	// Episode numbering in xmltv "onscreen" format: S01E02.
	// Skip for films — Pluto sets season/number to 1/1 for movies,
	// which confuses DVR software into treating them as series.
	if !isFilm(p) && p.Episode.Season > 0 && p.Episode.Number > 0 {
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
