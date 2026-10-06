CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A play, musical, concert or program, with its dates, people, mics,
-- what has to be ready and what's needed for it.
CREATE TABLE productions (
    id          INTEGER PRIMARY KEY,
    title       TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT '',  -- "Christmas musical", "Easter drama"
    status      TEXT NOT NULL DEFAULT 'planning'
                CHECK (status IN ('planning', 'rehearsing', 'ready', 'done', 'cancelled')),
    venue       TEXT NOT NULL DEFAULT '',
    director    TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Rehearsals and performances.
CREATE TABLE events (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'performance'
                  CHECK (kind IN ('performance', 'dress', 'tech', 'rehearsal', 'other')),
    date          TEXT NOT NULL,             -- YYYY-MM-DD
    time          TEXT NOT NULL DEFAULT '',  -- HH:MM, or '' for all day
    label         TEXT NOT NULL DEFAULT '',
    notes         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX events_production ON events(production_id, date, time);

CREATE TABLE scenes (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,
    number        TEXT NOT NULL DEFAULT '',  -- "1", "Act 2 Sc 1", "Song 4"
    title         TEXT NOT NULL DEFAULT '',
    notes         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX scenes_production ON scenes(production_id, position);

-- Who's in it: a character (or a part like "Choir left") and who plays it.
CREATE TABLE cast_members (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,
    character     TEXT NOT NULL DEFAULT '',
    person        TEXT NOT NULL DEFAULT '',
    notes         TEXT NOT NULL DEFAULT '',
    CHECK (character <> '' OR person <> '')
);
CREATE INDEX cast_production ON cast_members(production_id, position);

-- Mics used in the production. A mic can be a unit from the maintenance
-- tracker (its item and the ID on its sticker).
CREATE TABLE mics (
    id              INTEGER PRIMARY KEY,
    production_id   INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    position        INTEGER NOT NULL,
    channel         TEXT NOT NULL DEFAULT '',  -- console channel
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL DEFAULT 'lav' CHECK (kind IN ('lav', 'headset', 'handheld', 'other')),
    tracker_item_id INTEGER,
    tracker_unit    TEXT NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX mics_production ON mics(production_id, position);

-- Who has each mic in each scene.
CREATE TABLE mic_assignments (
    mic_id   INTEGER NOT NULL REFERENCES mics(id) ON DELETE CASCADE,
    scene_id INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    cast_id  INTEGER NOT NULL REFERENCES cast_members(id) ON DELETE CASCADE,
    PRIMARY KEY (mic_id, scene_id)
);
CREATE INDEX mic_assignments_scene ON mic_assignments(scene_id);
CREATE INDEX mic_assignments_cast ON mic_assignments(cast_id);

-- Each time the mic chart is marked final, a copy of it as it was.
CREATE TABLE mic_chart_versions (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    version       INTEGER NOT NULL,
    snapshot      TEXT NOT NULL,  -- JSON, see store.ChartSnapshot
    note          TEXT NOT NULL DEFAULT '',
    finalized_by  TEXT NOT NULL DEFAULT '',
    finalized_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (production_id, version)
);

-- Things that have to be ready: props, costumes, music, lighting cues...
CREATE TABLE prep_items (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    department    TEXT NOT NULL,
    name          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'in_progress', 'blocked', 'ready')),
    owner         TEXT NOT NULL DEFAULT '',
    due_on        TEXT NOT NULL DEFAULT '',
    scene_id      INTEGER REFERENCES scenes(id) ON DELETE SET NULL,
    notes         TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX prep_production ON prep_items(production_id, department);

-- Items and supplies the production needs, from the maintenance tracker
-- (a product or a supply there) or from somewhere else.
CREATE TABLE needs (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    quantity      INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
    department    TEXT NOT NULL DEFAULT '',
    tracker_kind  TEXT NOT NULL DEFAULT '' CHECK (tracker_kind IN ('', 'product', 'supply')),
    tracker_id    INTEGER,
    status        TEXT NOT NULL DEFAULT 'needed' CHECK (status IN ('needed', 'gathered', 'returned')),
    notes         TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK ((tracker_kind = '') = (tracker_id IS NULL))
);
CREATE INDEX needs_production ON needs(production_id);
