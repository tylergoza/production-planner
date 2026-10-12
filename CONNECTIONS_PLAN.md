# Connections as admin settings

The design is in `../event_planning/CONNECTIONS_PLAN.md`; read it first.
The event planner builds it first, so copy from there.

Status: planned, nothing built yet (2026-10-11).

Connections here: **Tracker** (already a setting with env override),
**Single sign-on** and **Calendar**.

## Steps

### 1. Settings plumbing
- [ ] Copy `internal/secrets` and the connection type from the event planner
- [ ] Plain `tracker_token` encrypted on start
- [ ] Whole-connection rule replaces today's per-field tracker override; tests

### 2. Connections page with the tracker
- [ ] `/admin/connections` with the tracker section (moved from Settings; old form redirects)
- [ ] **Test**, **Keep in settings**, "settings differ" warning, last changed by/when
- [ ] README **Connections** section

### 3. Single sign-on
- [ ] SSO client behind `atomic.Pointer`
- [ ] Pending values, **Test sign-in**, admin-role check, turn off with confirm
- [ ] Saved-but-broken SSO falls back to local login with a banner
- [ ] `production-planner connection clear sso|tracker|calendar`
- [ ] Tests as in the event planner

### 4. Calendar
- [ ] Build `CALENDAR_PLAN.md` step 2's settings as this connection, not env-only
- [ ] Backfill status and **Send all dates to the calendar** in its section (`CALENDAR_PLAN.md` step 3)

### 5. Ansible
- [ ] Connection variables optional in `vars.yml`; backup includes `secrets.key`; README hand-over steps
