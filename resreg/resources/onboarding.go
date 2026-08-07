package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// OnboardingRow mirrors the onboarding admission table (onbod/migrations/0004,
// store/migrations/0080) MINUS its credential columns. It is the projection
// every read surface — REST, MCP, OpenAPI, `arizuko export` — sees.
//
// token_ref is deliberately absent, not merely hidden with `yaml:"-"`: since
// the engine derives Insert's column list purely from the `db:` tags here, a
// field that does not exist cannot be exported, rendered, or logged by a later
// edit. It is also the reason this resource sets SkipApplyRebuild — see below.
// The bearer it hashes lives only in the /onboard?token=<raw> link the user was
// sent, so there is nothing here to leak even if a projection is added
// carelessly (Z3; same shape as route_tokens omitting token_hash).
//
// token_expires DOES appear: it is a timestamp, not a credential, and the
// operator queue page needs it to show which links are still live.
type OnboardingRow struct {
	JID          string `db:"jid"           yaml:"jid"                     json:"jid"`
	Status       string `db:"status"        yaml:"status"                  json:"status"`
	UserSub      string `db:"user_sub"      yaml:"user_sub,omitempty"      json:"user_sub,omitempty"`
	Gate         string `db:"gate"          yaml:"gate,omitempty"          json:"gate,omitempty"`
	Created      string `db:"created"       yaml:"created"                 json:"created"`
	PromptedAt   string `db:"prompted_at"   yaml:"prompted_at,omitempty"   json:"prompted_at,omitempty"`
	QueuedAt     string `db:"queued_at"     yaml:"queued_at,omitempty"     json:"queued_at,omitempty"`
	AdmittedAt   string `db:"admitted_at"   yaml:"admitted_at,omitempty"   json:"admitted_at,omitempty"`
	TokenExpires string `db:"token_expires" yaml:"token_expires,omitempty" json:"token_expires,omitempty"`
}

// OnboardingEndpoints mounts at the REAL served paths (onbod/main.go), not the
// PK-CRUD convention: approve and reprompt are state-machine transitions, not
// an `update` of arbitrary columns, so each gets its own named action rather
// than one overloaded PUT. This list is the single declaration the
// /openapi.json doc and onbod's mounted handler both read, so served routes and
// doc cannot drift.
var OnboardingEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/onboarding", Action: resreg.ActionCreate},
	{Verb: "GET", Path: "/v1/onboarding", Action: resreg.ActionList},
	{Verb: "POST", Path: "/v1/onboarding/{jid}/approve", Action: resreg.Action("approve")},
	{Verb: "POST", Path: "/v1/onboarding/{jid}/reprompt", Action: resreg.Action("reprompt")},
	{Verb: "DELETE", Path: "/v1/onboarding/{jid}", Action: resreg.ActionDelete},
}

// OnboardingMCPDoc gives the agent face its one-liners. Every action an
// operator can drive over REST is reachable over MCP too (5/17): the two faces
// are the same handler behind two injected gates.
var OnboardingMCPDoc = map[resreg.Action]string{
	resreg.ActionList:         "List pending and admitted onboarding rows — who is waiting to be let in, which gate queued them, and when their setup link expires. Filter with status= (awaiting_message, token_used, queued, approved). Never returns the setup link itself.",
	resreg.ActionCreate:       "Record an unrouted chat jid as awaiting onboarding, so the next poll tick sends it a setup link. Idempotent — re-recording a known jid changes nothing.",
	resreg.Action("approve"):  "Admit a queued jid immediately, bypassing its gate's daily limit. Use when someone should not wait out the queue.",
	resreg.Action("reprompt"): "Void a jid's outstanding setup link and issue a fresh one on the next tick. Use when the link expired or never arrived.",
	resreg.ActionDelete:       "Deny a jid and drop its onboarding row entirely. The jid can start over by messaging again.",
}

// OnboardingMCPArgs: jid is the PK for the per-row verbs; list takes an
// optional status filter. Create takes the jid to enqueue.
var OnboardingMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionList: {
		{Name: "status", Type: "string", Description: "Only rows in this status (awaiting_message, token_used, queued, approved). Omit for all."},
	},
	resreg.ActionCreate: {
		{Name: "jid", Type: "string", Description: "Chat jid to record as awaiting onboarding, e.g. telegram:user/12345.", Required: true},
	},
	resreg.Action("approve"): {
		{Name: "jid", Type: "string", Description: "Chat jid to admit immediately.", Required: true},
	},
	resreg.Action("reprompt"): {
		{Name: "jid", Type: "string", Description: "Chat jid whose setup link should be reissued.", Required: true},
	},
	resreg.ActionDelete: {
		{Name: "jid", Type: "string", Description: "Chat jid to deny and remove.", Required: true},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      OnboardingName,
		Table:     "onboarding",
		DB:        resreg.SubsystemOnbod,
		RowType:   reflect.TypeFor[OnboardingRow](),
		PKFields:  []string{"JID"},
		Endpoints: OnboardingEndpoints,
		MCPDoc:    OnboardingMCPDoc,
		MCPArgs:   OnboardingMCPArgs,
		// Admissions are runtime state driven by chat traffic and the gate
		// limiter, never declared in a manifest. Without this, any apply
		// mentioning onboarding would DELETE+INSERT the whole table from a
		// RowType that has no token_ref — nulling every live setup link
		// instance-wide (Z3). Mirrors route_tokens / invites / secrets.
		SkipApplyRebuild: true,
		Hooks: resreg.Hooks{
			// Every column below is nullable in onbod/migrations/0004 and NULL
			// is the NORMAL state: store.InsertOnboarding writes only (jid,
			// status, created), so the first pending admission on an instance
			// made ScanAll fail outright — and with it Export, Checksum, and
			// therefore `arizuko export`/`plan`/`apply`/`archive export` for
			// the whole onbod subsystem (BUGS F42).
			ColumnOverride: map[string]resreg.ColumnHook{
				"UserSub":      {Read: "COALESCE(user_sub, '')", Write: nilIfEmptyString},
				"Gate":         {Read: "COALESCE(gate, '')", Write: nilIfEmptyString},
				"PromptedAt":   {Read: "COALESCE(prompted_at, '')", Write: nilIfEmptyString},
				"QueuedAt":     {Read: "COALESCE(queued_at, '')", Write: nilIfEmptyString},
				"AdmittedAt":   {Read: "COALESCE(admitted_at, '')", Write: nilIfEmptyString},
				"TokenExpires": {Read: "COALESCE(token_expires, '')", Write: nilIfEmptyString},
			},
		},
	})
}
