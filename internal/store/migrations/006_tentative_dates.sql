-- Pencilled-in dates (BOOKING_PLAN.md step 1): held but not certain yet.
-- They go to the calendar as 'tentative', so the update trigger from 005
-- is rebuilt to also queue a date when it's pencilled in or confirmed.
ALTER TABLE events ADD COLUMN tentative INTEGER NOT NULL DEFAULT 0;

DROP TRIGGER calendar_event_update;

CREATE TRIGGER calendar_event_update
AFTER UPDATE OF kind, date, time, end_time, label, tentative ON events
WHEN OLD.kind IS NOT NEW.kind OR OLD.date IS NOT NEW.date OR OLD.time IS NOT NEW.time
  OR OLD.end_time IS NOT NEW.end_time OR OLD.label IS NOT NEW.label
  OR OLD.tentative IS NOT NEW.tentative
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;
