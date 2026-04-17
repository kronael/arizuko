CREATE TABLE IF NOT EXISTS onboarding_gates (
    gate        TEXT PRIMARY KEY,
    limit_per_day INTEGER NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1
);
