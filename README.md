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
| Schedule | Rehearsals, tech, dress and performances, with times and notes. |
| Cast & scenes | Characters (or parts like "Choir left") and who plays them, and the scenes in running order. Add one at a time or paste a list (`Mary - Ava Brooks`, or two columns from a spreadsheet). |
| Mic chart | Mics down, scenes across, who has each mic in each scene. The editor carries a choice forward to the following scenes, since actors usually keep a mic for a while. The chart shades **swaps** (a mic changing hands), flags anyone on **two mics** in one scene, lists the swaps going into each scene for the mic wrangler, and lists each person's mics and scenes. Prints landscape. |
| Final versions | **Mark final** saves the chart as v1, v2, and so on. Each version is kept and viewable, with what changed from the one before. The current chart shows *Draft*, *Final v2*, or *Changed since v2* with the changed cells outlined (hover to see what they were), so you know when the printed copy is out of date. |
| Mics | Add them by hand, or bring in every unit of a tracker product ("Lapel mics") in one click, named by the ID on its sticker. Set console channels and order. |
| Readiness | Things to get ready by department (props, costumes, set, music, sound effects, lighting, mics, slides & video, other), each with who's on it, a ready-by date, an optional scene, and a status (Not started, In progress, Needs help, Ready) changed right from the list. |
| Needs | Search the maintenance tracker's products and supplies and add how many you need. Each one shows how many the tracker has (on hand for supplies, with low/out flags) and **short by** when there aren't enough. Anything else is something to buy or borrow. Mark things Gathered, then Put back after the production. |
| Offline | Installable as an app. The dashboard, a production's overview and its mic chart open from the last copy when there's no signal backstage. |
| Users | Admins manage users and settings and can download a backup. Everything needs sign-in: cast lists aren't public. |

## The maintenance tracker

The planner only **reads** from the tracker, through its API (`/api/v1/`).

1. In the tracker, an admin opens **API**, makes a token named
   "Production planner" and copies it (`mt_…`).
2. In the planner, an admin opens **Settings**, enters the tracker's address
   and the token, and saves. Saving checks the connection.

Answers are cached for a minute. If the tracker can't be reached, pages
still work and say they couldn't check it. Needs and mics remember what they
were linked to by the tracker's IDs.

Not done yet: the tracker's API can't be written to, so the planner can't
reserve items or "Ask for more" of a supply there. That would need write
endpoints in the tracker first.

## Configuration

Set with a flag or an environment variable. Flags go **before** any command.

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8090` | Listen address |
| `DB_PATH` | `-db` | `data/productions.db` | SQLite file. Created, with migrations applied, on start. |
| `TZ` | | system | Church time zone: decides what "today", "upcoming" and "overdue" mean |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from a reverse proxy. Only when a proxy is the only way in. |
| `DEV=1` | `-dev` | off | Load templates/static from `./web` on disk |
| `TRACKER_URL`, `TRACKER_TOKEN` | | | The maintenance tracker connection. When set, they override Settings and can't be changed there. |

## Commands

```sh
production-planner create-user alice [--admin]   # prompts for a password (10+ characters)
production-planner reset-password alice
production-planner backup /path/backup.db        # safe while the app is running
production-planner seed-demo                     # a sample production
```

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
changes (keeping 10), installs, and checks `/healthz`.

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
cp deploy/ansible/vars.example.yml deploy/ansible/vars.yml   # set app_domain, dns_zone, tracker_url
export DIGITALOCEAN_TOKEN=...
export PP_TRACKER_TOKEN=mt_...         # optional; or connect the tracker in Settings
make provision                         # first time
make deploy                            # every update after that
```

On the first deploy the admin user is created before the app starts, so
`/setup` is never exposed. Set `PP_ADMIN_PASSWORD` to choose its password, or
a random one is printed at the end.

With a hardware SSH key, use a deploy key as described in the tracker's
README (`ssh_private_key_file` in `vars.yml`; the same key works for both).

### Moving between hosts

**Settings → Download backup** (or the `backup` command), stop the app on the
new host, copy the file to its database path
(`/var/lib/production-planner/productions.db`, owned by `planner`), and start it.
Droplet backups cover both apps when they share a droplet.

## Layout

```
main.go, seed.go            entrypoint, CLI commands, demo data
internal/store/             SQLite access, migrations, mic chart grid/swaps/versions
internal/tracker/           read-only client for the maintenance tracker's API
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
and an end-to-end run against a fake tracker API that renders every page,
including when the tracker is down.

## License

MIT. See [LICENSE](LICENSE).
