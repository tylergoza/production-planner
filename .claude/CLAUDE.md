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
- Deploys onto the maintenance tracker's droplet (found by tag
  `maintenance-tracker`). Caddy is split: `/etc/caddy/Caddyfile` only does
  `import /etc/caddy/sites/*.caddy`, and each app writes its own site file.
  The main Caddyfile text must stay identical in both repos' deploy.yml.
  `deploy/ansible/vars.yml` is gitignored and holds the real domain.
- The user runs all `git commit`s themselves (YubiKey signing). Don't commit.
