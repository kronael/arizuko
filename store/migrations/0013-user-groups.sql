CREATE TABLE IF NOT EXISTS user_groups (
    user_sub TEXT NOT NULL,
    folder   TEXT NOT NULL,
    PRIMARY KEY (user_sub, folder)
);
