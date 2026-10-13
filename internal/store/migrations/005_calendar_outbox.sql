-- Dates waiting to be sent to the church calendar (CALENDAR_PLAN.md step 2).
-- One row per date: a newer change replaces an older waiting one. The
-- worker builds what to send from the date as it is when it sends, so a row
-- only says "this date changed". No foreign key: a deleted date still needs
-- its DELETE sent.
--
-- The triggers below queue a row in the same transaction as any change the
-- calendar shows (times, kind, label, rooms, and the production's title,
-- public flag, lock and status). Rows queue even while no calendar is set
-- up; there's at most one per date, and connecting sends them.
CREATE TABLE calendar_outbox (
    event_id    INTEGER PRIMARY KEY,
    op          TEXT    NOT NULL CHECK (op IN ('put', 'delete')),
    -- Bumped on every queueing, so the worker only clears the row it sent
    -- and not a newer change made while it was sending.
    seq         INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    next_try_at TEXT    NOT NULL DEFAULT (datetime('now')),
    last_error  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX calendar_outbox_due ON calendar_outbox(next_try_at);

CREATE TRIGGER calendar_event_insert AFTER INSERT ON events BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_event_update
AFTER UPDATE OF kind, date, time, end_time, label ON events
WHEN OLD.kind IS NOT NEW.kind OR OLD.date IS NOT NEW.date OR OLD.time IS NOT NEW.time
  OR OLD.end_time IS NOT NEW.end_time OR OLD.label IS NOT NEW.label
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

-- Also fires for each date when a whole production is deleted (cascade).
CREATE TRIGGER calendar_event_delete AFTER DELETE ON events BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (OLD.id, 'delete', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

-- Rooms. Deleting a date deletes its rooms too; those mustn't turn its
-- 'delete' back into a 'put', hence the check that the date is still there.
CREATE TRIGGER calendar_room_insert AFTER INSERT ON event_rooms BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_room_update AFTER UPDATE OF place_id, place_name ON event_rooms
WHEN OLD.place_id IS NOT NEW.place_id OR OLD.place_name IS NOT NEW.place_name
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_room_delete AFTER DELETE ON event_rooms
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id)
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (OLD.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

-- What the calendar shows of a date that comes from its production: every
-- date goes again when one of these changes.
CREATE TRIGGER calendar_production_update
AFTER UPDATE OF title, public, locked, status ON productions
WHEN OLD.title IS NOT NEW.title OR OLD.public IS NOT NEW.public
  OR OLD.locked IS NOT NEW.locked OR OLD.status IS NOT NEW.status
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    SELECT id, 'put', (SELECT COALESCE(MAX(seq), 0) FROM calendar_outbox) + ROW_NUMBER() OVER (ORDER BY id)
    FROM events WHERE production_id = NEW.id;
END;

-- What the calendar said clashed with a date the last time it was sent
-- (CALENDAR_PLAN.md step 4). The push is asynchronous, so the worker keeps
-- the answer here for the schedule page. conflicts is JSON, opaque to the
-- store; no row means no clashes (or nothing sent yet).
CREATE TABLE calendar_conflicts (
    event_id   INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    conflicts  TEXT NOT NULL,
    checked_at TEXT NOT NULL DEFAULT (datetime('now'))
);
