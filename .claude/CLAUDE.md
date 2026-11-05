# Production Planner: project notes

Sibling of `../maintenance_tracking` (same stack, same conventions: Go +
SQLite + server-rendered templates + Stimulus autoloader, no build step).
When changing shared patterns (auth, CSRF, layout, Ansible), check whether
the tracker should get the same change.

- Reads from the tracker only through its read-only API (`internal/tracker`).
  Anything that needs to change the tracker (reserving items, "Ask for more")
  needs new write endpoints there first.
- Cookies are `pp_*` (the tracker's are `mt_*`) so both run side by side on
  localhost: planner on 8090, tracker on 8080.
- Mic chart status is derived, not stored: the current chart is compared
  with the latest row in `mic_chart_versions` (`ChartSnapshot.SameAs`).
- CSP is `style-src 'self'`: no inline `style=` attributes in templates.
- Each page template is parsed with only `layout.html` + `partials/*.html`,
  so anything shared between pages goes in a partial.
- Deploy (`deploy/ansible`): the app runs on an apps box (`apps_box`, default `apps-1`, shared with the other apps) in the church DigitalOcean account and binds its VPC address on port 8090; the edge box (`../edge_proxy`) holds Caddy, DNS points at it, and this deploy writes the app's site file there. Run commands through `with-do-church`. Its state is `productions.db` plus `secrets.key` only; `make backup` / `restore` / `move` carry both. Real `do_team` and domains live in the gitignored `vars.yml`.
- The user runs all `git commit`s themselves (YubiKey signing). Don't commit.
- `CALENDAR_PLAN.md` is this app's part of joining `../calendar` (8120);
  tick its checkboxes as steps are done. Everything there is optional:
  this app must keep working when `CALENDAR_URL` is unset.
- `CONNECTIONS_PLAN.md` (design in `../event_planning/CONNECTIONS_PLAN.md`): links to
  other apps are admin settings on `/admin/connections`; env vars override
  them per whole connection. Secrets encrypted with `secrets.key`.
- Every app must run alone. Links to other apps are optional and off
  unless their URL and token are set (the `Configured()` pattern); when
  off, the app acts as if the other app doesn't exist. Each app owns its
  own data; others keep only a thin copy or a reference. No hub app that
  everything depends on.
- Shared kinds of data (places/rooms, items, events, users) keep the same
  shape in every app, matching the owning app's API. A thin local version
  is a subset of the full one, so when the owning app is connected the
  local rows start referencing it (its ID plus a cached name) instead of
  being rebuilt.
- Pencilled-in dates (`events.tentative`) go to the calendar as `tentative`;
  a cancelled production's dates as `cancelled`, the rest `confirmed`.
  Rooms never filter on the tracker's `bookable` flag.
- `BOOKING_PLAN.md`: this app's part of bookable rooms / pending bookings / unscheduled events (design in `../.claude/plans/bookable-rooms-and-pending-bookings.md`). Tick its checkboxes as steps are done.
