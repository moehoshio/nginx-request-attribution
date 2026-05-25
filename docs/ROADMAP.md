# Roadmap

This file captures the agreed phased plan that grew out of the project
rename and parser refactor. It is the source of truth for "what comes
next"; smaller deferred items live in [`TODO.md`](./TODO.md).

## Status

| Phase | Title | Status |
|------:|-------|--------|
| 1 | Rename + parser refactor (Nginx / Apache / custom) | ✅ done |
| 2 | Account system (users, sessions, login, CSRF) | ✅ done |
| 3 | Settings panel + one-click restart | ✅ done |
| 4 | Directory watcher (recursive scan, rotation tracking) | ✅ done |
| 5 | Documentation / screenshots / integration tests | ✅ done |

## Phase 1 — Rename + parser refactor (shipped)

- Module path `github.com/moehoshio/nginx-request-attribution` →
  `github.com/moehoshio/web-request-attribution`; binary `nginx-req-attr`
  → `web-req-attr`; Dockerfile / `docker-compose.yml` / `deploy.sh` /
  four README translations updated.
- New `Parser` interface in `internal/parser` with `nginx`, `apache`,
  `custom`, and `auto` engines; `LogEntry` is unchanged.
- Custom engine accepts Nginx-style `$variable` tokens. Apache
  `%`-style `LogFormat` tokens are deferred (see `TODO.md`).
- Configuration uses a `sources: []` array; each source has its own
  `type` (`file` / `syslog`), `format`, and (for files) optional
  `read_compressed` to scan rotated `.gz` files once at startup.
- Per-engine parser tests cover Nginx combined / vhost-combined and
  Apache common / combined fixtures, plus a custom-pattern case.

## Phase 2 — Account system

Goal: gate the dashboard and API behind a login so the deployment can
be exposed to a network without leaking request data.

- **Schema** (new tables, additive migration):
  - `users(id, username, password_hash, role, created_at, updated_at, last_login_at, disabled)`
  - `sessions(id, user_id, token_hash, created_at, expires_at, ip, user_agent)`
  - `audit_log(id, user_id, action, target, ip, created_at, detail)`
- **Package** `internal/auth`:
  - bcrypt password hashing (cost configurable, default 12)
  - Random 256-bit session tokens, stored hashed; cookie is
    `HttpOnly`, `SameSite=Lax`, `Secure` when behind TLS.
  - CSRF: double-submit cookie. Non-`GET`/`HEAD` requests must echo
    the CSRF cookie via `X-CSRF-Token`.
  - `RequireAuth` and `RequireAdmin` middleware.
- **Bootstrap admin**: on first launch, if `users` is empty and
  `config.auth.bootstrap_admin` is set, create that admin user once.
- **HTTP API** (all JSON):
  - `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/me`,
    `GET /api/auth/csrf`
  - `GET /api/users`, `POST /api/users`, `PATCH /api/users/{id}`,
    `DELETE /api/users/{id}` (admin only)
  - `POST /api/users/me/password` (any signed-in user)
- **UI**: login screen + a small "Users" admin panel; the existing
  dashboard becomes the post-login landing.
- **Tests**: hashing round-trip, session create/validate/expire,
  middleware allow / deny, bootstrap idempotency.

## Phase 3 — Settings panel + one-click restart (shipped)

Runtime configuration (sources, keywords, watch toggle) lives in the
`runtime_config` SQLite row and is edited via the admin-only **Settings**
tab. Bootstrap fields (`listen_addr`, `db_path`, `auth.bootstrap_admin`,
`allowed_log_roots`) stay in `config.json` and still require a restart;
the panel surfaces them as read-only context.

- `internal/runtimeconfig` exposes a `Store` with `Get` / `Set` /
  `Subscribe`. Values are persisted as a JSON blob in a single-row table.
- `internal/watcher.Manager` subscribes to the store and diffs the
  running set of file / syslog sources: new sources start, removed
  sources stop, changed sources restart. Toggling `watch` off tears
  everything down; flipping it back on rebuilds the set.
- `allowed_log_roots` is enforced at validation time so a settings edit
  cannot point a `file` source at a path outside the configured roots
  (`..` traversal is normalised out by `filepath.Clean`/`filepath.Rel`).
- API: `GET /api/config` returns `{runtime, bootstrap, schema}`;
  `PUT /api/config` accepts a new `runtime` document; `POST
  /api/admin/restart` re-execs the binary with `os.Executable()` +
  `syscall.Exec` on Linux and falls back to `os.Exit(0)` everywhere
  else so an orchestrator (Docker / systemd) can bring the process
  back. Both endpoints require admin and CSRF.
- UI: schema-driven Settings tab with per-source rows, "Add file /
  syslog source" buttons, "Save", "Reload", and "Restart server"
  buttons. Restart polls `/api/auth/me` until the server comes back
  and then reloads.

## Phase 4 — Directory watcher

Goal: replace bespoke per-file configuration with a `type: "dir"`
source that handles rotation correctly.

- A new `type: "dir"` source scans a directory tree for files whose
  basename matches a glob (e.g. `access*.log*`). `recursive: true`
  descends into subdirectories; otherwise only the top level is
  scanned.
- `file_state(path, inode, size, offset, mtime, fingerprint)` tracks
  per-file position so the watcher survives restart. Rotation is
  detected via inode change, size shrink (truncate), or a content
  fingerprint mismatch — the last one covers filesystems that reuse
  inodes (e.g. tmpfs).
- One-shot import of rotated `.gz` archives when
  `read_compressed: true`; subsequent scans skip already-imported
  archives by recording `offset = size`. Live tailing for the active
  plain files.
- Integrates with the settings panel (Phase 3): directory sources are
  configured from the UI like file/syslog sources, with extra
  inputs for the filename glob and recursion toggle.

## Phase 5 — Documentation / screenshots / integration tests

- All four READMEs rewritten end-to-end for the Web (Nginx + Apache +
  custom) positioning, with screenshots of the new dashboard and the
  settings / users pages.
- Reference docs for the custom-format variable table (Nginx tokens
  today, Apache `%`-tokens once `TODO.md` items are addressed).
- An integration test that boots the server in-process, ingests a
  fixture log, logs in, hits the dashboard API, and edits a setting.

## Risks / notes

- The rename in Phase 1 was a breaking change. Forks pinned to the old
  module path keep working (Go modules don't auto-redirect), so the
  READMEs continue to mention the previous name.
- Apache `%t` and Nginx `$time_local` share the
  `[10/Oct/2000:13:55:36 -0700]` format and can reuse one time parser.
- Apache-specific tokens such as `%D` (microseconds) and `%T` (seconds)
  remain unsupported until the custom engine grows `%`-token support.
- Future dependencies (`golang.org/x/crypto/bcrypt`, possibly
  `github.com/ulikunitz/xz`) are screened against the GitHub advisory
  database before being added.
- Because there is no existing deployment, schema changes are made
  additively without data-migration steps.
