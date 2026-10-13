# Connections as admin settings

The design is in `../event_planning/CONNECTIONS_PLAN.md`; read it first.
The event planner builds it first, so copy from there.

Status: all steps built. Steps 1, 2, 4 and 5 on 2026-10-12; step 3 (SSO) on 2026-10-13.

Connections here: **Tracker** (already a setting with env override),
**Single sign-on** and **Calendar**.

## Steps

### 1. Settings plumbing
- [x] Copy `internal/secrets` and the connection type from the event planner
- [x] Plain `tracker_token` encrypted on start
- [x] Whole-connection rule replaces today's per-field tracker override; tests

### 2. Connections page with the tracker
- [x] `/admin/connections` with the tracker section (moved from Settings; old form redirects)
- [x] **Test**, **Keep in settings**, "settings differ" warning, last changed by/when
- [x] README **Connections** section

### 3. Single sign-on
- [x] SSO client behind `atomic.Pointer`
- [x] Pending values, **Test sign-in**, admin-role check, turn off with confirm
- [x] Saved-but-broken SSO falls back to local login with a banner
- [x] `production-planner connection clear sso|tracker|calendar`
- [x] Tests as in the event planner

### 4. Calendar
- [x] Build `CALENDAR_PLAN.md` step 2's settings as this connection, not env-only
- [x] Backfill status and **Send all dates to the calendar** in its section (`CALENDAR_PLAN.md` step 3)

### 5. Ansible
- [x] Connection variables optional in `vars.yml`; backup includes `secrets.key`; README hand-over steps
