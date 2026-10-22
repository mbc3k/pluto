package tve

type amcChannel struct {
	requestor string
	domain    string
	group     string
	liveID    string
}

// Public AMC Networks playback identifiers. The Adobe requestor is the
// resource id their player asks Adobe to authorize.
var amcChannels = map[string]amcChannel{
	"amc":  {requestor: "AMC", domain: "amc.com", group: "1", liveID: "6398764467112"},
	"bbca": {requestor: "BBCA", domain: "bbcamerica.com", group: "6", liveID: "6398760324112"},
	"ifc":  {requestor: "IFC", domain: "ifc.com", group: "7", liveID: "6398760311112"},
	"wetv": {requestor: "WETV", domain: "wetv.com", group: "9", liveID: "6398685705112"},
}

type warnerChannel struct {
	brand   string
	mediaID string
	page    string
}

var warnerChannels = map[string]warnerChannel{
	"tnt-east": {brand: "tnt", mediaID: "tnt-east", page: "https://www.tntdrama.com/watchtnt/east"},
	"tnt-west": {brand: "tnt", mediaID: "tnt-west", page: "https://www.tntdrama.com/watchtnt/east"},
	"tbs-east": {brand: "tbs", mediaID: "tbs-east", page: "https://www.tbs.com/watchtbs/east"},
	"tbs-west": {brand: "tbs", mediaID: "tbs-west", page: "https://www.tbs.com/watchtbs/east"},
	"tru-east": {brand: "trutv", mediaID: "tru-east", page: "https://www.trutv.com/watchtrutv/east"},
	"tru-west": {brand: "trutv", mediaID: "tru-west", page: "https://www.trutv.com/watchtrutv/east"},
	"tcm-east": {brand: "tcm", mediaID: "tcm-east", page: "https://www.tcm.com/watchtcm/livestream"},
	"tcm-west": {brand: "tcm", mediaID: "tcm-west", page: "https://www.tcm.com/watchtcm/livestream"},
}

type foxNewsChannel struct {
	requestor string
	resource  string
	master    string
	page      string
	generator string
}

var foxNewsChannels = map[string]foxNewsChannel{
	"fox_news": {
		requestor: "foxnews",
		resource:  `<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>Fox News Channel</title><item><title>Fox News Channel</title><guid>2056</guid></item></channel></rss>`,
		master:    "https://247.foxnews.com/hls/live/2003586/FNCHLSv3/master.m3u8",
		page:      "https://www.foxnews.com/",
		generator: "https://tve-auth.foxnews.com/atk/token/generator",
	},
	"fox_business": {
		requestor: "foxbusiness",
		resource:  `<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>Fox Business Network</title><item><title>Fox Business Network</title><guid>2176</guid></item></channel></rss>`,
		master:    "https://247.foxbusiness.com/hls/live/2003756/FBNHLSv3/master.m3u8",
		page:      "https://www.foxbusiness.com/",
		generator: "https://tve-auth.foxnews.com/atk/token/generator",
	},
}

var foxSportsRequestor = "fbc-fox"

var foxSportsCall = map[string]string{
	"fox_sports_fs1":  "FS1",
	"fox_sports_fs2":  "FS2",
	"fox_sports_fox":  "FOX",
	"fox_sports_btn":  "BTN",
	"fox_deportes":    "FOXDEP",
	"fox_soccer_plus": "FSP",
}

type discoveryChannel struct {
	id   string
	code string
}

var discoveryChannels = map[string]discoveryChannel{
	"tlc":           {id: "161", code: "TLC"},
	"food-network":  {id: "162", code: "FOOD"},
	"discovery":     {id: "163", code: "DSC"},
	"id":            {id: "165", code: "IDS"},
	"animal-planet": {id: "167", code: "APL"},
	"own":           {id: "168", code: "OWN"},
	"hgtv":          {id: "172", code: "HGTV"},
	"cooking":       {id: "174", code: "COOK"},
	"travel":        {id: "175", code: "TRAV"},
	"magnolia":      {id: "222", code: "MAG"},
}

func playSpecFor(sourceID, channelID string) (playSpec, bool) {
	switch sourceID {
	case SourceAMC:
		ch, ok := amcChannels[channelID]
		if !ok {
			return playSpec{}, false
		}
		return playSpec{source: sourceID, channel: channelID, kind: "amc", requestor: ch.requestor, resource: ch.requestor}, true
	case SourceWarner:
		ch, ok := warnerChannels[channelID]
		if !ok {
			return playSpec{}, false
		}
		return playSpec{source: sourceID, channel: channelID, kind: "warner", requestor: ch.brand, resource: ch.brand}, true
	case SourceFox:
		if ch, ok := foxNewsChannels[channelID]; ok {
			return playSpec{source: sourceID, channel: channelID, kind: "foxnews", requestor: ch.requestor, resource: ch.resource}, true
		}
		if sign, ok := foxSportsCall[channelID]; ok {
			return playSpec{source: sourceID, channel: channelID, kind: "foxsports", requestor: foxSportsRequestor, resource: sign}, true
		}
		return playSpec{}, false
	case SourceDiscovery:
		ch, ok := discoveryChannels[channelID]
		if !ok {
			return playSpec{}, false
		}
		return playSpec{source: sourceID, channel: channelID, kind: "discovery", requestor: "discovery", resource: ch.code}, true
	default:
		return playSpec{}, false
	}
}
