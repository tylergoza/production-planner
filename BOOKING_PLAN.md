# Production Planner: productions not scheduled yet, and pencilled-in dates

The cross-app design is in
`../.claude/plans/bookable-rooms-and-pending-bookings.md`; this is the
production planner's part. All of it works without the calendar. With the
calendar connected, pencilled-in dates hold rooms there as `tentative`.

Status: not started (planned 2026-10-13).

## The idea

A production can already exist with no dates ("No dates yet"). That
becomes the **Not scheduled yet** stage, shown on purpose instead of
by accident. What's missing is a way to **hold** a date before it's
certain:

| Stage | Dates | On the calendar |
|-------|-------|-----------------|
| **Not scheduled yet** | None | Nothing sent |
| **Pencilled in** | Dates marked `tentative` | Sent with status `tentative`: blocks the rooms, shown as "Pencilled in" |
| **Scheduled** | Confirmed dates | Sent as `confirmed`, as today |

Pencilled in is set **per date** (`events.tentative`). A production can
have a confirmed run of performances and still-pencilled extra
rehearsals. The production's `status` (`planning`, `rehearsing`, …) is
unchanged.

**Rooms**: production and date rooms keep coming from every tracker
building, room and area (plus typed-in rooms). They must **not** filter on
the tracker's new `bookable` flag, which is only for the calendar's public
request form.

## Steps

### 1. Pencilled-in dates
- [ ] Migration `006_tentative_dates.sql`: `ALTER TABLE events ADD COLUMN tentative INTEGER NOT NULL DEFAULT 0`. Drop and recreate the `005` outbox update trigger on `events` so it also watches `tentative`
- [ ] Date form (`web/templates/partials/forms.html`): **Pencilled in (not confirmed yet)** checkbox. Schedule page: a "Pencilled in" label on those dates, and **Confirm** per date plus **Confirm all pencilled-in dates** for the production
- [ ] When adding several dates at once (if the schedule form does), one checkbox applies to all of them
- [ ] `calendarBooking` (`internal/server/calendar_sync.go`): status `tentative` when the date is, `cancelled` when the production is cancelled, else `confirmed`. Update the status comment in `internal/calendar/calendar.go`
- [ ] Public pages and `.ics` (if any list dates): pencilled-in dates labelled, `STATUS:TENTATIVE`
- [ ] Room clash warnings (`internal/server/calendar_rooms.go`) show other apps' `tentative` / `pending` bookings with their label (they're already busy blocks)
- [ ] Tests: push status per combination, outbox fires on confirm, confirm-all

### 2. Not scheduled yet
- [ ] Dashboard and `/productions`: a **Not scheduled yet** group for active productions with no dates (today they sort as key "2"); a production whose dates are all pencilled in shows "Dates not confirmed"
- [ ] A production moving to `rehearsing` or `ready` with no dates, or only pencilled-in performances, gets a warning on its page (not a refusal)
- [ ] Tests: grouping and warnings

### 3. Docs
- [ ] README: the stages; `CALENDAR_PLAN.md`: the status rule now includes `tentative`
- [ ] `.claude/CLAUDE.md`: "pencilled-in dates go to the calendar as `tentative`"
