package playlist

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/mbc3k/pluto/internal/pluto"
)

const stitcherBase = "https://cfd-v4-service-channel-stitcher-use1-1.prd.pluto.tv"

// Approx bytes per EXTINF + URL line; used to pre-size the builder.
const avgBytesPerChannel = 450

// Generate builds an M3U playlist for the given credentials. Each channel
// entry embeds the JWT so Channels DVR can open the stream without
// additional authentication.
//
// token and stitcherParams should be obtained once via Session.Credentials()
// so the session lock is not acquired once per channel.
func Generate(startChannel int, token, stitcherParams string, channels []pluto.Channel) string {
	var b strings.Builder
	b.Grow(16 + len(channels)*avgBytesPerChannel)
	b.WriteString("#EXTM3U\n")

	// Query suffix shared by every stream URL (credentials don't vary per channel).
	// Pattern: ?{stitcherParams}&jwt={token}&masterJWTPassthrough=true&includeExtendedEvents=true
	stitcherParams = strings.TrimPrefix(strings.TrimSpace(stitcherParams), "?")
	stitcherParams = strings.TrimPrefix(stitcherParams, "&")

	var query strings.Builder
	query.Grow(len(stitcherParams) + len(token) + 64)
	query.WriteByte('?')
	if stitcherParams != "" {
		query.WriteString(stitcherParams)
		query.WriteByte('&')
	}
	query.WriteString("jwt=")
	query.WriteString(url.QueryEscape(token))
	query.WriteString("&masterJWTPassthrough=true&includeExtendedEvents=true")
	querySuffix := query.String()

	const pathPrefix = stitcherBase + "/v2/stitch/hls/channel/"
	const pathSuffix = "/master.m3u8"

	for i := range channels {
		ch := &channels[i]
		chNum := startChannel + i
		name := m3uText(ch.Name)

		// %q keeps attribute values safely quoted if names contain special chars.
		fmt.Fprintf(&b,
			"#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q tvg-chno=%d channel-id=%q group-title=%q,%s\n",
			m3uText(ch.Slug),
			name,
			m3uText(ch.ColorLogoPNG.Path),
			chNum,
			m3uText(ch.ID),
			m3uText(ch.Category),
			name,
		)
		b.WriteString(pathPrefix)
		b.WriteString(url.PathEscape(ch.ID))
		b.WriteString(pathSuffix)
		b.WriteString(querySuffix)
		b.WriteByte('\n')
	}

	return b.String()
}

// m3uText keeps a field on one physical line. A raw newline in a channel
// name would otherwise start a new playlist entry.
func m3uText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
