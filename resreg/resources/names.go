package resources

// names.go: every resource name, spelled once (spec 5/16). A resource's Name
// is its wire identity, the `/v1/<name>` REST path AND the MCP tool prefix,
// and both the catalog registration and the owning daemon's mounted handler
// read these constants. A string literal at either site is the drift vector
// (proxyd's live route resource carried "routes" while its catalog said
// proxyd_routes); name_source_test.go fails on a re-introduced literal.
const (
	ACLName               = "acl"
	ACLMembershipName     = "acl_membership"
	AuditName             = "audit"
	ChatsName             = "chats"
	DeadLettersName       = "dead_letters"
	GDriveFilesName       = "gdrive_files"
	GitHubSourcesName     = "github_sources"
	GroupsName            = "groups"
	InstalledPackagesName = "installed_packages"
	InvitesName           = "invites"
	NetworkRulesName      = "network_rules"
	PendingActionsName    = "pending_actions"
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
