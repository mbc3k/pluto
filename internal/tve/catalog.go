package tve

import "github.com/mbc3k/pluto/internal/source"

// Source IDs the process knows how to serve. Each one is its own playlist
// and its own XMLTV document.
const (
	SourceAE         = "aenetworks"
	SourceAMC        = "amcn"
	SourceWarner     = "warner"
	SourceFox        = "fox"
	SourceDiscovery  = "discovery"
	combinedSourceID = "all"
)

// Known reports whether id is a source this build can enable.
func Known(id string) bool {
	_, ok := catalogs[id]
	return ok
}

// DisplayName is the label shown on the status page.
func DisplayName(id string) string {
	if c, ok := catalogs[id]; ok {
		return c.name
	}
	if id == combinedSourceID {
		return "All TV Everywhere"
	}
	return id
}

type catalog struct {
	name     string
	channels []source.Channel
}

// catalogs are public lineups. Stream paths that start with "/" are filled
// in by the server when the playlist is served. Absolute URLs are the
// upstream manifest and do not need a DirecTV media token.
var catalogs = map[string]catalog{
	SourceAE: {
		name: "A+E Networks",
		channels: []source.Channel{
			{ID: "history", Name: "History", Category: "Entertainment", Logo: "https://www.history.com/assets/images/history/apple-touch-icon.png", StreamURL: "https://dai.google.com/linear/hls/event/3pCfCAVSTz24VQ7jZDXLzw/master.m3u8"},
			{ID: "aetv", Name: "A&E", Category: "Entertainment", Logo: "https://www.aetv.com/assets/images/aetv/apple-touch-icon.png", StreamURL: "https://dai.google.com/linear/hls/event/4elNLJnvT_ifl3Qs6G67yQ/master.m3u8"},
			{ID: "lifetime", Name: "Lifetime", Category: "Movies", Logo: "https://www.mylifetime.com/assets/images/lifetime/apple-touch-icon.png", StreamURL: "https://dai.google.com/linear/hls/event/E54WsMQ2RjCPjg5DriaUCA/master.m3u8"},
			{ID: "fyi", Name: "FYI", Category: "Lifestyle", Logo: "https://www.aetv.com/assets/images/fyi/apple-touch-icon.png", StreamURL: "https://dai.google.com/linear/hls/event/-umds2mLSeCPi0vUgua6Kg/master.m3u8"},
		},
	},
	SourceAMC: {
		name: "AMC Networks",
		channels: []source.Channel{
			{ID: "amc", Name: "AMC", Category: "Entertainment", Logo: "https://cdn.amcnetworks.com/amc/theme/web/amc_logo_bk_bg.png", StreamURL: "/tve/amcn/amc/stream"},
			{ID: "bbca", Name: "BBC America", Category: "Entertainment", Logo: "https://cdn.amcnetworks.com/default-imgs/bbca/bbca-square.jpg", StreamURL: "/tve/amcn/bbca/stream"},
			{ID: "ifc", Name: "IFC", Category: "Entertainment", Logo: "https://cdn.amcnetworks.com/default-imgs/ifc/ifc-square.jpg", StreamURL: "/tve/amcn/ifc/stream"},
			{ID: "wetv", Name: "WE tv", Category: "Entertainment", Logo: "https://cdn.amcnetworks.com/default-imgs/wetv/wetv-square-2024.jpg", StreamURL: "/tve/amcn/wetv/stream"},
		},
	},
	SourceWarner: {
		name: "Warner",
		channels: []source.Channel{
			{ID: "tnt-east", Name: "TNT (East)", Category: "Entertainment", StreamURL: "/tve/warner/tnt-east/stream"},
			{ID: "tnt-west", Name: "TNT (West)", Category: "Entertainment", StreamURL: "/tve/warner/tnt-west/stream"},
			{ID: "tbs-east", Name: "TBS (East)", Category: "Entertainment", StreamURL: "/tve/warner/tbs-east/stream"},
			{ID: "tbs-west", Name: "TBS (West)", Category: "Entertainment", StreamURL: "/tve/warner/tbs-west/stream"},
			{ID: "tru-east", Name: "truTV (East)", Category: "Entertainment", StreamURL: "/tve/warner/tru-east/stream"},
			{ID: "tru-west", Name: "truTV (West)", Category: "Entertainment", StreamURL: "/tve/warner/tru-west/stream"},
			{ID: "tcm-east", Name: "TCM (East)", Category: "Movies", StreamURL: "/tve/warner/tcm-east/stream"},
			{ID: "tcm-west", Name: "TCM (West)", Category: "Movies", StreamURL: "/tve/warner/tcm-west/stream"},
		},
	},
	SourceFox: {
		name: "FOX",
		channels: []source.Channel{
			{ID: "fox_news", Name: "Fox News Channel", Category: "News", StreamURL: "/tve/fox/fox_news/stream"},
			{ID: "fox_business", Name: "Fox Business Network", Category: "Business", StreamURL: "/tve/fox/fox_business/stream"},
			{ID: "fox_weather", Name: "Fox Weather", Category: "News", StreamURL: "https://content.uplynk.com/db88cebb80ea46269a1b2691871f3660.m3u8"},
			{ID: "fox_sports_fs1", Name: "FS1", Category: "Sports", StreamURL: "/tve/fox/fox_sports_fs1/stream"},
			{ID: "fox_sports_fs2", Name: "FS2", Category: "Sports", StreamURL: "/tve/fox/fox_sports_fs2/stream"},
			{ID: "fox_sports_fox", Name: "FOX Sports", Category: "Sports", StreamURL: "/tve/fox/fox_sports_fox/stream"},
			{ID: "fox_sports_btn", Name: "Big Ten Network", Category: "Sports", StreamURL: "/tve/fox/fox_sports_btn/stream"},
			{ID: "fox_deportes", Name: "FOX Deportes", Category: "Sports", StreamURL: "/tve/fox/fox_deportes/stream"},
			{ID: "fox_soccer_plus", Name: "FOX Soccer Plus", Category: "Sports", StreamURL: "/tve/fox/fox_soccer_plus/stream"},
		},
	},
	SourceDiscovery: {
		name: "Discovery",
		channels: []source.Channel{
			{ID: "tlc", Name: "TLC", Category: "Lifestyle", StreamURL: "/tve/discovery/tlc/stream"},
			{ID: "food-network", Name: "Food Network", Category: "Food", StreamURL: "/tve/discovery/food-network/stream"},
			{ID: "discovery", Name: "Discovery Channel", Category: "Documentary", StreamURL: "/tve/discovery/discovery/stream"},
			{ID: "hgtv", Name: "HGTV", Category: "Lifestyle", StreamURL: "/tve/discovery/hgtv/stream"},
			{ID: "id", Name: "Investigation Discovery", Category: "Crime", StreamURL: "/tve/discovery/id/stream"},
			{ID: "animal-planet", Name: "Animal Planet", Category: "Animals", StreamURL: "/tve/discovery/animal-planet/stream"},
			{ID: "own", Name: "OWN", Category: "Entertainment", StreamURL: "/tve/discovery/own/stream"},
			{ID: "travel", Name: "Travel Channel", Category: "Travel", StreamURL: "/tve/discovery/travel/stream"},
			{ID: "cooking", Name: "Cooking Channel", Category: "Food", StreamURL: "/tve/discovery/cooking/stream"},
			{ID: "magnolia", Name: "Magnolia Network", Category: "Lifestyle", StreamURL: "/tve/discovery/magnolia/stream"},
		},
	},
}

func cloneChannels(id string) []source.Channel {
	c, ok := catalogs[id]
	if !ok {
		return nil
	}
	out := make([]source.Channel, len(c.channels))
	copy(out, c.channels)
	return out
}

// adobeRequestor is the Adobe Pass requestor used to start a DirecTV login
// for sources whose streams are authorized that way. Fox's public news
// manifests and Discovery's own login are not in this map.
func adobeRequestor(sourceID string) string {
	switch sourceID {
	case SourceAE:
		return "HISTORY"
	case SourceAMC:
		return "AMC"
	case SourceWarner:
		return "TNT"
	default:
		return ""
	}
}
