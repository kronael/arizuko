-- Schema parity with routd migration 0041 so store's github_sources read/write
-- paths (gitd, the CLI, tests) see the same columns routd owns.
CREATE TABLE github_sources (
    source       TEXT PRIMARY KEY,
    secret       TEXT NOT NULL,
    owner_folder TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);
