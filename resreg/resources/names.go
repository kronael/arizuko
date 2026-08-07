package resources

// names.go — every resource name, spelled once (spec 5/16 §"One owner +
// federation").
//
// A resource's Name is its wire identity: the `/v1/<name>` REST path AND the
// MCP tool prefix. Each resource is instantiated as a resreg.Resource TWICE —
// the catalog registration below in this package (shape only, no handler) and
// the owning daemon's mounted handler (which adds Store/Handler/Gate). Both
// read these constants, so the two declarations cannot disagree about what the
// resource is called.
//
// A string literal at either site is the drift vector: proxyd's live route
// resource once carried Name: "routes" while its own catalog and /openapi.json
// said proxyd_routes (fixed 2026-07-01). name_source_test.go fails on a
// re-introduced literal, in any package.
const (
	ACLName               = "acl"
	ACLMembershipName     = "acl_membership"
	AuditName             = "audit"
	GroupsName            = "groups"
	InstalledPackagesName = "installed_packages"
	InvitesName           = "invites"
	NetworkRulesName      = "network_rules"
	OnboardingName        = "onboarding"
	OnboardingGatesName   = "onboarding_gates"
	ProxydRoutesName      = "proxyd_routes"
	RouteTokensName       = "route_tokens"
	RoutesName            = "routes"
	ScheduledTasksName    = "scheduled_tasks"
	SecretsName           = "secrets"
	SessionsName          = "sessions"
	SigningKeysName       = "signing_keys"
	WebRoutesName         = "web_routes"
)
