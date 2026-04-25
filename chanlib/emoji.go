package chanlib

// ClassifyEmoji returns "like" or "dislike" based on the emoji's
// approximate sentiment. Unknown emojis default to "like" — the
// adapter is signaling "someone reacted, here's what they used"; the
// agent gets the actual emoji string in InboundMsg.Reaction for nuance.
func ClassifyEmoji(emoji string) string {
	if negativeEmoji[emoji] {
		return "dislike"
	}
	return "like"
}

var negativeEmoji = map[string]bool{
	"👎": true,
	"💩": true,
	"😡": true,
	"🤬": true,
	"💔": true,
	"🤮": true,
	"😢": true,
}
