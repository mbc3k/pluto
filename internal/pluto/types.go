package pluto

import "time"

// AuthResponse is the JSON response from the Pluto TV boot endpoint.
// GET https://boot.pluto.tv/v4/start?appName=web&...&username=...&password=...
type AuthResponse struct {
	SessionToken   string `json:"sessionToken"`
	StitcherParams string `json:"stitcherParams"`
}

// ChannelResponse is the top-level JSON response from the channels API.
// GET https://api.pluto.tv/v2/channels?start=...&stop=...
// The API returns a JSON array directly (not wrapped in an object).
type ChannelResponse []Channel

// Channel represents a single Pluto TV channel.
type Channel struct {
	ID          string    `json:"_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Number      float64   `json:"number"`
	Category    string    `json:"category"`
	Summary     string    `json:"summary"`
	IsStitched  bool      `json:"isStitched"`
	ColorLogoPNG ImageRef `json:"colorLogoPNG"`
	FeaturedImage ImageRef `json:"featuredImage"`
	Timelines   []Program `json:"timelines"`
}

// ImageRef holds a path to an image asset.
type ImageRef struct {
	Path string `json:"path"`
}

// Program represents a single program in a channel's timeline.
type Program struct {
	ID      string    `json:"_id"`
	Title   string    `json:"title"`
	Start   time.Time `json:"start"`
	Stop    time.Time `json:"stop"`
	Episode Episode   `json:"episode"`
}

// Episode holds per-episode metadata within a program.
type Episode struct {
	ID          string  `json:"_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Season      int     `json:"season"`
	Number      int     `json:"number"`
	Genre       string  `json:"genre"`
	SubGenre    string  `json:"subGenre"`
	Rating      string  `json:"rating"`
	Series      Series  `json:"series"`
	Poster      ImageRef `json:"poster"`
}

// Series holds series-level metadata.
type Series struct {
	ID   string `json:"_id"`
	Type string `json:"type"`
}
