# Church Production Planner

Plan the church's plays, musicals and programs: their rehearsal and
performance dates, the cast and scenes, the **mic chart** (with every
finalized version kept), what each department has to get **ready**, and the
items and supplies **needed**, picked from the
[maintenance tracker](../maintenance_tracking) so you can see how many there
are and where.

Built the same way as the maintenance tracker:

- **Backend:** Go, one static binary with its templates, JS and CSS embedded
- **Database:** SQLite (one file, pure-Go driver, no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev), vendored, with the
  same no-build autoloader. No Node, no bundler.

## Quick start

Needs Go 1.27 or newer.

```sh
make dev                     # http://127.0.0.1:8090, edits to web/ show on refresh
go run . seed-demo           # optional: a sample Christmas musical
```

The first visit goes to `/setup` to create the admin account. It runs on
port 8090, so the maintenance tracker (8080) can run alongside it.

## Features

| Area | What it does |
|------|--------------|
| Dashboard | Each production in the works: its next date, how much is ready, needs gathered, and mic chart version. Then every date coming up. |
| Productions | Title, kind, where, director, notes, and a status (Planning, Rehearsing, Ready, Done, Cancelled). Done and cancelled ones leave the dashboard but are kept. The overview flags readiness items that need help or are past due, and lists what's still needed. |
| Schedule | Rehearsals, tech, dress and performances, with start and end times, rooms and notes. A new date starts with the production's usual rooms (picked from the tracker's places, or typed in). Two dates wanting the same room at the same time get a **Double-booked** warning, never a refusal. |
| Cast & scenes | Characters (or parts like "Choir left") and who plays them, and the scenes in running order. Add one at a time or paste a list (`Mary - Ava Brooks`, or two columns from a spreadsheet). |
| Mic chart | Mics down, scenes across, who has each mic in each scene. The editor carries a choice forward to the following scenes, since actors usually keep a mic for a while. The chart shades **swaps** (a mic changing hands), flags anyone on **two mics** in one scene, lists the swaps going into each scene for the mic wrangler, and lists each person's mics and scenes. Prints landscape. |
| Final versions | **Mark final** saves the chart as v1, v2, and so on. Each version is kept and viewable, with what changed from the one before. The current chart shows *Draft*, *Final v2*, or *Changed since v2* with the changed cells outlined (hover to see what they were), so you know when the printed copy is out of date. |
| Mics | Add them by hand, or bring in every unit of a tracker product ("Lapel mics") in one click, named by the ID on its sticker. Set console channels and order. |
| Readiness | Things to get ready by department (props, costumes, set, music, sound effects, lighting, mics, slides & video, other), each with who's on it, a ready-by date, an optional scene, and a status (Not started, In progress, Needs help, Ready) changed right from the list. |
| Needs | Search the maintenance tracker's products and supplies and add how many you need. Each one shows how many the tracker has (on hand for supplies, with low/out flags) and **short by** when there aren't enough. Anything else is something to buy or borrow. Mark things Gathered, then Put back after the production. |
| Offline | Installable as an app. The dashboard, a production's overview and its mic chart open from the last copy when there's no signal backstage. |
| Users | Admins manage users, settings and connections and can download a backup. Everything needs sign-in: cast lists aren't public. |
| Church calendar | Optional. With the calendar connected, every date goes onto the church calendar with its rooms, and room warnings cover the other apps too (see below). |
| Access | A production is open to everyone signed in, or locked by an admin to a list of people (under **Edit details**). Others don't see a locked production at all; admins see every production. |

## The maintenance tracker

The planner only **reads** from the tracker, through its API (`/api/v1/`).

1. In the tracker, an admin opens **API**, makes a token named
   "Production planner" and copies it (`mt_…`).
2. In the planner, an admin opens **Connections**, enters the tracker's
   address and the token, and saves. Saving checks the connection.

Answers are cached for a minute. If the tracker can't be reached, pages
still work and say they couldn't check it. Needs and mics remember what they
were linked to by the tracker's IDs.

Not done yet: the tracker's API can't be written to, so the planner can't
reserve items or "Ask for more" of a supply there. That would need write
endpoints in the tracker first.

## The church calendar

Optional, and off until it's connected: without it nothing below happens.
With it, every rehearsal and performance is a booking on the church
calendar (`../calendar`), so a dress rehearsal and a fellowship dinner
can't both have the Lifecenter without someone being told.

1. On the calendar, an admin makes a **source** token for `productions` on
   its API tokens page (`cal_…`).
2. In the planner, an admin opens **Connections**, fills in the calendar's
   address (and on a shared droplet the internal address,
   `http://127.0.0.1:8120`) and the token, and saves.
3. On the same page, **Send all dates to the calendar** sends every date
   from a year ago on. Pressing it again is safe.

What's sent for each date: "{production}: {label or kind}" (e.g.
"Christmas Musical: Dress rehearsal"), the kind, the times, the rooms, a
link to the production's schedule (when `BASE_URL` is set), and whether it's
cancelled (the whole production is). Performances of a production marked
**Public** are public on the calendar, with the production's "About it" as
their description; rehearsals, tech and dress are always private. A locked
production's dates are sent with no title, kind or description, only rooms
and times. Notes, cast, mics and readiness are never sent.

A date with a start time and no end time is sent as lasting three hours
(and checked for room clashes that way); one with no start time is all day.

Sending happens in the background: every change is queued with the save
itself and sent straight after, so a save never waits on the calendar. If
the calendar can't be reached, it tries again after a minute, then 5, 30
and 60. **Connections** shows how many dates are waiting and the last
error. Changing a production's title, Public, lock or status sends all its
dates again; deleting a date or a production removes them from the
calendar.

Room warnings on the schedule come from the calendar while it's connected
(bookings from every app; other apps' private ones show as "Booked ·
2:00–4:00 pm"). If it can't be reached, the schedule falls back to this
app's own check and says so.

## Connections

The maintenance tracker, single sign-on and the church calendar are set on
**/admin/connections** (admins only), or on the server with environment
variables. A connection with **any** of its variables set comes wholly from
the environment: the page shows it as *Set on the server* and can't change
it. Otherwise it's whatever was saved on the page, or off. Tokens are
write-only on the page and stored encrypted with `secrets.key`, a key file
the app makes next to the database on first start; keep the two together.

To hand a server-set connection over to the app: press **Keep in settings**
(the page then says the settings match), blank the variables on the server
and restart. Nothing disconnects. If a saved connection is ever the problem,
`production-planner connection clear sso|tracker|calendar` forgets it.

Single sign-on saves differently, so a typo can't lock admins out: see
*Setting it up on the Connections page* under Single sign-on.

## Configuration

Set with a flag or an environment variable. Flags go **before** any command.

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8090` | Listen address |
| `DB_PATH` | `-db` | `data/productions.db` | SQLite file. Created, with migrations applied, on start. |
| `TZ` | | system | Church time zone: decides what "today", "upcoming" and "overdue" mean |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from a reverse proxy. Only when a proxy is the only way in. |
| `DEV=1` | `-dev` | off | Load templates/static from `./web` on disk |
| `TRACKER_URL`, `TRACKER_TOKEN` | | | The maintenance tracker connection. When either is set, the whole connection comes from here and can't be changed on Connections. |
| `CALENDAR_URL`, `CALENDAR_INTERNAL_URL`, `CALENDAR_TOKEN` | | | The church calendar connection: its public address, an optional server-to-server address (`http://127.0.0.1:8120` on the shared droplet), and a `cal_…` source token for `productions`. Same whole-connection rule. |
| `SSO_URL` | | | User Management's public address (e.g. `https://accounts.example.org`). Set it to sign in there instead of with local passwords. With all the SSO variables (and `BASE_URL`) empty, single sign-on is whatever was saved on Connections. |
| `SSO_INTERNAL_URL` | | `SSO_URL` | User Management for server-to-server calls, e.g. `http://127.0.0.1:8100` on the shared droplet |
| `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET` | | | From registering this app in User Management (shown once). Required when `SSO_URL` is set; the app won't start without them. |
| `BASE_URL` | | from the request | This app's public address, e.g. `https://plan.example.org`. The sign-in redirect URI is `BASE_URL/auth/callback` and must be registered exactly like that in User Management. Also the link the calendar gets back to each production. Part of the single sign-on connection: setting it alone puts that connection on the server (and so off). |

## Commands

```sh
production-planner create-user alice [--admin]   # prompts for a password (10+ characters)
production-planner reset-password alice           # also gives SSO users a local password, and turns them back on
production-planner local-login on|off             # break-glass, see below
production-planner connection clear sso|tracker|calendar   # forget a saved connection
production-planner backup /path/backup.db        # safe while the app is running
production-planner seed-demo                     # a sample production
```

## Single sign-on

With `SSO_URL` set, people sign in with their church account in User
Management (the `user_management` repo) instead of a password here:

1. `/login` sends the browser to User Management's `/authorize`
   (authorization code + PKCE). The state and PKCE verifier wait in a
   10-minute `pp_sso` cookie.
2. User Management sends it back to `/auth/callback` with a code. The app
   checks the state, swaps the code for a grant (server to server), and
   adds or updates the person's local user row by their User Management ID.
   An existing local user with the same username is linked, so their
   history stays theirs. Role `admin` there means admin here.
3. Every 5 minutes at most, a session's grant is checked again, which picks
   up name and role changes. If User Management says the sign-in has
   ended (signed out, turned off, access removed), the session ends here
   too. If it can't be reached, sessions keep working for an hour past
   their last good check, so restarting it signs no one out.
4. Signing out here also signs out of User Management, and so of the
   other apps within 5 minutes.

**Setting it up (or changing it) on the Connections page.** Single sign-on
can also be saved in the app instead of `SSO_URL` and friends. A wrong
setting could lock every admin out, so saving never switches it on:

1. Admin › Connections › Single sign-on: fill in the User Management
   address, optional internal address, client ID, client secret and this
   app's address, and **Save**. The values are stored as *pending* (the
   secret encrypted) and sign-in doesn't change. A blank secret keeps the
   current one.
2. Press **Test sign-in**. The app first checks the client ID and secret
   with User Management server to server, then sends you through User
   Management and back. Only if that works **and** you come back as an
   `admin` of this app does the pending set become live; your local
   account is linked to your church account like the first rollout does.
   Otherwise the page says why (for example "you signed in as X, who isn't
   an admin of this app in User Management"), the old setting stays and the
   pending values wait. You stay signed in here either way. **Discard**
   drops the pending values.
3. **Turn off single sign-on** (after a confirm) goes back to local
   passwords. Anyone who only ever signed in through User Management has
   none; set one with `production-planner reset-password USERNAME`.
4. Values set in the environment are shown read-only; **Keep in settings**
   copies them over, as for the other connections.

Break-glass if a bad saved setting still gets in the way:
`production-planner local-login on` brings the password form back, and
`production-planner connection clear sso` removes the saved settings, pending
ones included. A broken saved setting falls back to local login with a
banner for admins; broken environment values still stop the app at start.

`/setup` is gone and `/account` links to User Management's account page.
Production members and other planner permissions stay in this app.

**Users page.** With SSO on, Admin › Users (`/admin/users`) is the app-admin
page for this app's people, read from and changed through User
Management's API. It lists everyone with access to the planner: username,
name, role, whether they're active, suspended or turned off, and who last
changed it. An admin can change someone's role (any role the planner has
there) or suspend and restore their access. Suspending signs them out of
the planner straight away. User Management checks every change against
its own records, as the signed-in admin, and the page shows its answer if
it says no (e.g. "make someone else an admin first" for the last admin).
Rows a user admin has locked are read-only. There's no adding people,
passwords, renames or deleting here: a user admin does those in User
Management, and user admins get a "Manage in User Management" link. An
admin signed in with the break-glass local login sees the list read-only.

Opening the Users page or a people picker (a production's members) pulls
the list from User Management (reused for a minute) and updates the local
users, so people show up before they've ever signed in here. Anyone whose
access is gone keeps their local row, so their history stays theirs, but
is marked inactive: they stay on productions they were already a member
of, marked "(no access)", and can't be newly added. If User Management
can't be reached, the pickers use the last list.

**Break-glass.** If User Management is broken, an admin can bring the
password form back over SSH, without a restart:

```sh
sudo -u planner production-planner -db /var/lib/production-planner/productions.db local-login on
sudo -u planner production-planner -db /var/lib/production-planner/productions.db reset-password alice
```

People who only ever signed in through SSO have no password here, so give
the admin one with `reset-password` (which also turns their account here
back on, in case their access was removed). `/login` then shows the password form
(with a link to sign in through User Management as well). Run
`local-login off` once User Management is back.

## Deploying

The app is one binary plus one `.db` file, behind [Caddy](https://caddyserver.com)
for HTTPS. `deploy/` has the systemd unit and a Caddyfile for doing it by
hand. Follow the comments at the top of `deploy/production-planner.service`.

### DigitalOcean with Ansible

`deploy/ansible/` works like the maintenance tracker's, and by default puts
the planner **on the tracker's droplet**: it finds it by the tracker's tag
(`maintenance-tracker`) and adds itself alongside, on port 8090 with its own
service user (`planner`), data folder and Caddy site. Provision adds the DNS
record for the planner's domain (pointing at the same droplet) and the
service user; with no tagged droplet it would create one. Deploy runs the
tests, builds the Linux binary here, backs up the database when the binary
changes (keeping 10 sets: the database and `secrets.key` together, as
`pre-deploy-<time>.db` and `.key` in the backup folder), installs, and
checks `/healthz`.

Caddy's config is split so both apps can deploy without overwriting each
other: `/etc/caddy/Caddyfile` only imports `/etc/caddy/sites/*.caddy`, and
each app writes its own site there (`production-planner.caddy`,
`maintenance-tracker.caddy`). Both repos write the same main Caddyfile. If
the droplet still has the Caddyfile from an older tracker deploy, the
planner's deploy stops and asks you to deploy the updated tracker first,
rather than taking the tracker offline.

For a droplet of its own instead, set `droplet_name` and `do_tag` to
`production-planner` in `vars.yml`.

```sh
cp deploy/ansible/vars.example.yml deploy/ansible/vars.yml   # set app_domain, dns_zone
export DIGITALOCEAN_TOKEN=...
make provision                         # first time
make deploy                            # every update after that
```

On the first deploy the admin user is created before the app starts, so
`/setup` is never exposed. Set `PP_ADMIN_PASSWORD` to choose its password, or
a random one is printed at the end.

The maintenance tracker and the church calendar connections are optional.
Leave each blank in `vars.yml` to manage it in the app on `/admin/connections`.
To set one in `vars.yml` instead, fill in its address and let `with-pp` pass
the token from the Keychain: `tracker_url` with `PP_TRACKER_TOKEN` (the
tracker's "Production planner" API token), or `calendar_url` with
`PP_CALENDAR_TOKEN` (the calendar's source token for `productions`, Keychain
item `production-planner-calendar`). On the shared droplet set
`calendar_internal_url` to `http://127.0.0.1:8120`.

First deploy with connections in `vars.yml`:

1. `with-do with-pp make deploy`.
2. Sign in, open **/admin/connections**, and press **Keep in settings** for each connection set in `vars.yml`. The app saves it (tokens are encrypted with `secrets.key`).
3. Blank those values in `vars.yml` and redeploy. The app keeps its saved settings.
4. With the calendar set, press **Send all dates to the calendar** on `/admin/connections` once.

Backups include `secrets.key`, which the saved tokens need, so restore both
files together. Neither playbook touches `secrets.key`. If it's deleted, the
app makes a new one and the saved tokens have to be entered again.

#### Turning on single sign-on

This is the server-side way; you can instead leave `sso_url` blank and set
SSO up on `/admin/connections` (see Single sign-on above), or set it here
first and hand it over with **Keep in settings** like the other
connections. To set it here: once User
Management is deployed on the same droplet and its import-users playbook has
printed the planner's client secret:

```sh
# in deploy/ansible/vars.yml:
#   sso_url: https://accounts.example.org
#   base_url: https://productions.example.org   # sign-in returns to base_url/auth/callback
export DIGITALOCEAN_TOKEN=...
export UM_PLANNER_SECRET=...           # from User Management's import-users playbook
make deploy
```

The deploy stops before building if `sso_url` is set without the secret or
`base_url`, so keep `UM_PLANNER_SECRET` exported for every deploy while SSO
is on. The secret goes only into the root-only systemd drop-in, with
nothing logged. To turn SSO back off (or hand it to the Connections page),
remove `sso_url` and deploy again. If
User Management is down, use the `local-login on` break-glass above instead.

With a hardware SSH key, use a deploy key as described in the tracker's
README (`ssh_private_key_file` in `vars.yml`; the same key works for both).

### Moving between hosts

**Settings → Download backup** (or the `backup` command), stop the app on the
new host, copy the file to its database path
(`/var/lib/production-planner/productions.db`, owned by `planner`) with
`secrets.key` from the old host beside it, and start it. Without the key the
app starts fine, but saved tokens have to be entered again on Connections.
Droplet backups cover both apps when they share a droplet.

## Layout

```
main.go, seed.go            entrypoint, CLI commands, demo data
internal/store/             SQLite access, migrations, mic chart grid/swaps/versions
internal/tracker/           read-only client for the maintenance tracker's API
internal/calendar/          client for the church calendar's push API
internal/secrets/           encrypts saved tokens (secrets.key)
internal/server/            routes, middleware, handlers, PWA endpoints
web/templates/              layout, partials, pages, sw.js
web/static/                 css, js (application, autoloader, controllers, vendor), icons
deploy/                     systemd unit, Caddyfile, ansible/ (DigitalOcean provision + deploy)
```

## Tests

```sh
make test
```

They cover the mic chart (swaps, clashes, per-person scene ranges, final
versions and what changed), pasted lists, reordering, production summaries,
rooms and room clashes, the calendar outbox and what's sent (a locked
production sends no title, a rehearsal is never public), connections, and an
end-to-end run against a fake tracker API that renders every page,
including when the tracker is down.

## License

MIT. See [LICENSE](LICENSE).
