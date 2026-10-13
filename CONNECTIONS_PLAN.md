# Connections as admin settings

The design is in `../event_planning/CONNECTIONS_PLAN.md`; read it first.
The event planner builds it first, so copy from there.

Status: steps 1, 2, 4 and 5 built (2026-10-12) for the tracker and calendar. Step 3 (SSO) not done: SSO is still set on the server only.

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
- [ ] SSO client behind `atomic.Pointer`
- [ ] Pending values, **Test sign-in**, admin-role check, turn off with confirm
- [ ] Saved-but-broken SSO falls back to local login with a banner
- [ ] `production-planner connection clear sso|tracker|calendar` (tracker and calendar done; sso waits for step 3)
- [ ] Tests as in the event planner

### 4. Calendar
- [x] Build `CALENDAR_PLAN.md` step 2's settings as this connection, not env-only
- [x] Backfill status and **Send all dates to the calendar** in its section (`CALENDAR_PLAN.md` step 3)

### 5. Ansible
- [x] Connection variables optional in `vars.yml`; backup includes `secrets.key`; README hand-over steps
