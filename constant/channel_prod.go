package constant

// Prod-only channel types live far above upstream's range. Upstream hands out the
// next free number for every new type, and sharing that range meant renumbering
// these (and the prod channels rows) each time it reached them.
const (
	ChannelTypeAIHorde = 1001 // async image-gen task adaptor
	ChannelTypeRunware = 1002 // sync image-gen adaptor, addresses Civitai checkpoints by AIR
)

var prodChannelBaseURLs = map[int]string{
	ChannelTypeAIHorde: "https://aihorde.net",
	ChannelTypeRunware: "https://api.runware.ai",
}

func init() {
	ChannelTypeNames[ChannelTypeAIHorde] = "AI Horde"
	ChannelTypeNames[ChannelTypeRunware] = "Runware"
}
