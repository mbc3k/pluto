package epg

import (
	"encoding/xml"
	"fmt"
	"strings"
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
	ID          string     `xml:"id,attr"`
	DisplayName []langText `xml:"display-name"`
	Icon        *icon      `xml:"icon,omitempty"`
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
	System string   `xml:"system,attr"`
	Value  langText `xml:"value"`
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
			tl := &ch.Timelines[j]
			if tl.Start.IsZero() || !tl.Stop.After(tl.Start) {
				continue
			}
			doc.Programmes = append(doc.Programmes, buildProgramme(ch.Slug, ch.Category, tl))
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
	name := xmlSafe(ch.Name)
	if name == "" {
		name = xmlSafe(ch.Slug)
	}
	xch := xChannel{
		ID: xmlSafe(ch.Slug),
		DisplayName: []langText{
			{Lang: "en", Value: name},
		},
	}
	if src := xmlSafe(ch.ColorLogoPNG.Path); src != "" {
		xch.Icon = &icon{Src: src}
	}
	return xch
}

// isFilm returns true if the program's series type indicates a movie/film.
func isFilm(p *pluto.Program) bool {
	return p.Episode.Series.Type == "film"
}

// programmeCategory returns the XMLTV category for a program.
// Films get "Movie" so DVR software files them correctly.
// Other programs get the episode genre and the channel category when those
// differ from categories already added.
func programmeCategory(channelCategory string, p *pluto.Program) []langText {
	var cats []langText
	if isFilm(p) {
		cats = addCategory(cats, "Movie")
	}
	cats = addCategory(cats, p.Episode.Genre)
	cats = addCategory(cats, p.Episode.SubGenre)
	cats = addCategory(cats, channelCategory)
	return cats
}

func addCategory(cats []langText, value string) []langText {
	value = xmlSafe(value)
	if value == "" {
		return cats
	}
	for _, c := range cats {
		if c.Value == value {
			return cats
		}
	}
	return append(cats, langText{Lang: "en", Value: value})
}

func buildProgramme(channelSlug, channelCategory string, p *pluto.Program) programme {
	title := xmlSafe(p.Title)
	if title == "" {
		title = xmlSafe(p.Episode.Name)
	}
	if title == "" {
		title = "Unknown"
	}
	prog := programme{
		Start:   formatXMLTVTime(p.Start),
		Stop:    formatXMLTVTime(p.Stop),
		Channel: xmlSafe(channelSlug),
		Title: []langText{
			{Lang: "en", Value: title},
		},
	}

	// Episode name as sub-title when present and distinct from title.
	if name := xmlSafe(p.Episode.Name); name != "" && name != title {
		prog.SubTitle = []langText{
			{Lang: "en", Value: name},
		}
	}

	if desc := xmlSafe(p.Episode.Description); desc != "" {
		prog.Desc = []langText{
			{Lang: "en", Value: desc},
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

	if rated := xmlSafe(p.Episode.Rating); rated != "" {
		prog.Rating = &rating{
			System: "VCHIP",
			Value:  langText{Lang: "en", Value: rated},
		}
	}

	return prog
}

func formatXMLTVTime(t time.Time) string {
	return t.UTC().Format(xmltvTimeFormat)
}

// xmlSafe strips characters XML 1.0 cannot represent. One bad title would
// otherwise fail the whole document and block the guide update.
func xmlSafe(s string) string {
	if s == "" || strings.IndexFunc(s, illegalXMLRune) == -1 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if illegalXMLRune(r) {
			return -1
		}
		return r
	}, s)
}

func illegalXMLRune(r rune) bool {
	return !(r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF))
}
