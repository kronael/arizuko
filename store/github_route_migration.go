package store

import (
	"context"
	"strings"

	"github.com/kronael/arizuko/core"
)

// MigrateGitHubRouteTopics adds the chat JID to unpinned GitHub routes once.
func (s *Store) MigrateGitHubRouteTopics(ctx context.Context) error {
	return s.migrateRouteTargets(ctx, "github-route-topics", func(route core.Route) string {
		if !strings.Contains(route.Target, "#") {
			for _, predicate := range strings.Fields(route.Match) {
				if strings.HasPrefix(predicate, "chat_jid=github:") {
					return route.Target + "#{jid}"
				}
			}
		}
		return route.Target
	})
}
