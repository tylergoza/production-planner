-- A locked production is only seen by its members (and admins). An
-- unlocked one is open to everyone signed in; its member list is kept so
-- locking it again brings the same people back.
ALTER TABLE productions ADD COLUMN locked INTEGER NOT NULL DEFAULT 0;

CREATE TABLE production_members (
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (production_id, user_id)
);
CREATE INDEX production_members_user ON production_members(user_id);
