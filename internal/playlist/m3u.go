package playlist

import (
	"fmt"
	"strings"

	"github.com/mbc3k/pluto/internal/auth"
	"github.com/mbc3k/pluto/internal/pluto"
)

const stitcherBase = "https://cfd-v4-service-channel-stitcher-use1-1.prd.pluto.tv"

// Generate builds an M3U playlist for the given tuner session. Each channel
// entry embeds the tuner's device ID and JWT so Channels DVR can open the
// stream without additional authentication.
func Generate(startChannel int, session *auth.Session, channels []pluto.Channel) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")

	for i, ch := range channels {
		chNum := startChannel + i
		streamURL := buildStreamURL(ch, session)

		fmt.Fprintf(&b,
			"#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q tvg-chno=%d channel-id=%q group-title=%q,%s\n%s\n",
			ch.Slug,
			ch.Name,
			ch.ColorLogoPNG.Path,
			chNum,
			ch.ID,
			ch.Category,
			ch.Name,
			streamURL,
		)
	}

	return b.String()
}

// buildStreamURL constructs the authenticated HLS stream URL for a channel.
// The stitcherParams from the auth response are appended as-is (they already
// contain the device identification query string). The JWT and passthrough
// flags are appended after.
func buildStreamURL(ch pluto.Channel, s *auth.Session) string {
	// URL pattern:
	// {stitcherBase}/v2/stitch/hls/channel/{channelID}/master.m3u8?{stitcherParams}&jwt={token}&masterJWTPassthrough=true&includeExtendedEvents=true
	base := fmt.Sprintf("%s/v2/stitch/hls/channel/%s/master.m3u8", stitcherBase, ch.ID)

	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteByte('?')

	stitcherParams := s.StitcherParams()
	if stitcherParams != "" {
		sb.WriteString(stitcherParams)
		sb.WriteByte('&')
	}

	sb.WriteString("jwt=")
	sb.WriteString(s.Token())
	sb.WriteString("&masterJWTPassthrough=true&includeExtendedEvents=true")

	return sb.String()
}
