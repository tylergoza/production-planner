-- Rooms and end times, so each date can be booked on the church calendar
-- (CALENDAR_PLAN.md step 1). Both are useful without the calendar too.

-- When a date finishes. '' means "until done".
ALTER TABLE events ADD COLUMN end_time TEXT NOT NULL DEFAULT '';  -- HH:MM

-- Performances of a public production show on the public calendar.
ALTER TABLE productions ADD COLUMN public INTEGER NOT NULL DEFAULT 0;

-- The rooms a production usually uses; a new date starts with these.
-- place_id is the maintenance tracker's place, NULL for a room typed in by
-- hand. place_name keeps the tracker's path so pages work without it.
CREATE TABLE production_rooms (
    id            INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL REFERENCES productions(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,
    place_id      INTEGER,
    place_name    TEXT NOT NULL
);
CREATE INDEX production_rooms_production ON production_rooms(production_id, position);

-- The rooms each date uses.
CREATE TABLE event_rooms (
    id         INTEGER PRIMARY KEY,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    place_id   INTEGER,
    place_name TEXT NOT NULL
);
CREATE INDEX event_rooms_event ON event_rooms(event_id, position);
CREATE INDEX event_rooms_place ON event_rooms(place_id);
