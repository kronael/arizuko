package chanlib

// ClassifyEmoji returns "like" or "dislike" by approximate sentiment; unknown
// emojis default to "like", and the agent gets the actual emoji in
// InboundMsg.Reaction.
func ClassifyEmoji(emoji string) string {
	if negativeEmoji[emoji] {
		return "dislike"
	}
	return "like"
}

var negativeEmoji = map[string]bool{
	"thumbsdown":                 true,
	"-1":                         true,
	"poop":                       true,
	"shit":                       true,
	"hankey":                     true,
	"rage":                       true,
	"face_with_symbols_on_mouth": true,
	"broken_heart":               true,
	"face_vomiting":              true,
	"cry":                        true,
	"👎":                          true,
	"💩":                          true,
	"😡":                          true,
	"🤬":                          true,
	"💔":                          true,
	"🤮":                          true,
	"😢":                          true,
}

// VerdictEmoji reads a reaction as an approval verdict, or "" when it is an
// ordinary reaction. A reaction is the ONE answer surface every chat platform
// has, so it is how a held call is resolved where buttons do not draw.
//
// The set is green/red only. 👍 and 👎 are deliberately absent: they are
// ordinary social reactions, and reading one as "run the gated call" is an
// approval nobody gave.
func VerdictEmoji(emoji string) string {
	switch emoji {
	case "✅", "✔", "✔️", "🟢", "☑️":
		return "/approve"
	case "❌", "✖", "✖️", "🔴", "🚫", "⛔":
		return "/reject"
	}
	return ""
}
