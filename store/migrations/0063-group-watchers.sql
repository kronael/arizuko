-- observe_group MCP tool: directional cross-folder ambient context.
-- observer receives messages delivered to source as <observed> context
-- on its next trigger turn. One row per (observer, source) pair.
-- Spec: specs/5/F-topic-lineage.md § observe_group.
CREATE TABLE IF NOT EXISTS group_watchers (
    observer TEXT NOT NULL,
    source   TEXT NOT NULL,
    PRIMARY KEY (observer, source)
);
