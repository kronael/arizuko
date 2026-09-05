-- gitd delivery sources (spec 6/27): one row per webhook secret — a repo hook
-- (repo/<owner>/<repo>), an org hook (org/<owner>) or a GitHub App (app/<slug>).
-- The webhook path /github/<source> selects the row; the secret is sealed under
-- SECRETS_KEY like a `secrets` value. Which folder an event reaches is the
-- ordinary routes table — the JID carries owner + repo, so one App secret
-- serves every repo of an installation and no per-repo mapping lives here.
CREATE TABLE github_sources (
    source       TEXT PRIMARY KEY,
    secret       TEXT NOT NULL,
    owner_folder TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);
