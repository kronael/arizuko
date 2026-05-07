CREATE TABLE IF NOT EXISTS web_routes (
    path_prefix TEXT PRIMARY KEY,
    access TEXT NOT NULL CHECK(access IN ('public','auth','deny','redirect')),
    redirect_to TEXT,
    folder TEXT NOT NULL,
    created_at TEXT NOT NULL
);
