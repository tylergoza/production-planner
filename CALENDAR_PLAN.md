# Production Planner: joining the Church Calendar

The design is in `../calendar/PLAN.md`; read its "push API" section first.
This file is the production planner's part. The sending side works the
same way as the event planner's (`../event_planning/CALENDAR_PLAN.md`), so
copy from there once it's built.

Status: steps 1–4 built (2026-10-12); the Ansible side of step 5 is in `deploy.yml`, the deploy itself is still to do.

## What changes

With the **Calendar** connection set (address and token, on `/admin/connections`, or `CALENDAR_URL` / `CALENDAR_TOKEN` on the server, which win; see `CONNECTIONS_PLAN.md`), every rehearsal and
performance goes onto the church calendar with its rooms, so a dress
rehearsal and a fellowship dinner can't both have the Lifecenter without
someone being told.

**Without them set, nothing changes**, except the two small additions below
that are useful on their own.

## What the planner needs first

Today a production has one free-text `venue`, and its dates (`events`
table) have a start time but no end time and no rooms. A booking needs
both. So:

- **End time** on each date (optional; blank means "until done", sent as
  start + 3 hours with a note in the README, or as all-day when there's no
  start time).
- **Rooms** on each date, picked from the tracker's places like the event
  planner does, defaulting to the production's rooms. The production gets a
  rooms list (replacing what `venue` is used for, but keep `venue` as the
  display text for anything that isn't a tracker room).
- **Public** on the production (off by default). Performances of a public
  production are `public`; rehearsals, tech and dress are always `private`;
  a locked production's dates are always `locked`.

## How sending works

Same as the event planner: a `calendar_outbox` keyed by date ID, written in
the same transaction as any change, a worker with back-off, never blocking
a save. `source` is `productions`, `source_id` is the date's ID. Title is
"{production title}: {label or kind}" ("Christmas Musical: Dress
rehearsal"). `url` is the production's schedule page. For locked
productions send no title, kind or description. Never send notes, cast,
mics or readiness.

Changing a production's title, public flag, lock or status re-queues all
its dates. A cancelled production sends its dates as cancelled; otherwise a
pencilled-in date (`events.tentative`, BOOKING_PLAN.md) is sent as
`tentative` and the rest as `confirmed`. Pencilling in or confirming a date
re-queues it. Deleting
a production or a date sends `delete`.

## Steps

### 1. Dates get end times and rooms
- [x] Migration `004_date_rooms.sql`: `events.end_time`, `production_rooms` (production_id, position, place_id, place_name), `event_rooms` (event_id, place_id, place_name), `productions.public`
- [x] Add `Places`/`Rooms` to `internal/tracker` (copy from the event planner)
- [x] Production form: rooms picker (copy the event planner's room search controller) and **Public**
- [x] Schedule form: end time, rooms (prefilled from the production)
- [x] Same-app room clash warning on the schedule, like the event planner's
- [x] Tests: rooms default from the production; end time optional

### 2. Client, outbox and worker
- [x] `internal/calendar` client (copy from the event planner)
- [x] **Calendar** connection (address, internal address, token) built the `CONNECTIONS_PLAN.md` way: on `/admin/connections`, with `CALENDAR_URL`, `CALENDAR_INTERNAL_URL`, `CALENDAR_TOKEN` overriding; README settings table
- [x] Migration `005_calendar_outbox.sql`, outbox writes in the store, worker
- [x] Tests: locked production sends no title; rehearsal never public; title change re-queues every date

### 3. Send everything (the backfill)
A button in the calendar section of the admin **Connections** page, the same as the event planner's (copy it):

- [x] In the calendar section of `/admin/connections` (admins only): waiting rows, last error, last successful send (connected or not and **Test** come from the connection itself)
- [x] **Send all dates to the calendar** button: POST with CSRF, confirm showing the count, queues every date from one year ago on in one transaction, the worker sends them
- [x] Safe to press again (one outbox row per date)
- [x] Tests: queues the right dates; pressing twice leaves one row per date; non-admins get 403

### 4. Room warnings across apps
- [x] Schedule form asks the calendar's `availability` when it's set; falls back to the same-app check
- [x] Show returned `conflicts` after a save as a warning

### 5. Deploy
- [ ] Either set the connection on `/admin/connections`, or for the first deploy use Ansible vars `calendar_url` and the token from the Keychain (`with-pp` gets `PP_CALENDAR_TOKEN` too), then **Keep in settings** and blank them
- [ ] After the first deploy with the calendar set, press **Send all dates to the calendar** on `/admin/connections`
