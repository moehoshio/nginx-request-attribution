# Roadmap

This file captures the agreed phased plan that grew out of the project
rename and parser refactor. It is the source of truth for "what comes
next"; smaller deferred items live in [`TODO.md`](./TODO.md).

## Status

| Phase | Title | Status |
|------:|-------|--------|
| 1 | Rename + parser refactor (Nginx / Apache / custom) | ✅ done |
| 2 | Account system (users, sessions, login, CSRF) | 🚧 in progress |
| 3 | Settings panel + one-click restart | ⏳ planned |
| 4 | Directory watcher (recursive scan, rotation tracking) | ⏳ planned |
| 5 | Documentation / screenshots / integration tests | ⏳ planned |

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

## Phase 3 — Settings panel + one-click restart

Goal: move runtime configuration into the database and edit it from
the UI; only true bootstrap fields stay in `config.json`.

- `config.json` keeps: `db_path`, `listen_addr`,
  `auth.bootstrap_admin`, `allowed_log_roots`.
- Everything else (sources, keywords, watch toggle, …) moves to a
  `runtime_config` table, exposed via a `ConfigStore` that supports
  subscribe / change-notification.
- Watcher manager subscribes to source changes and hot-reloads:
  start new sources, stop removed ones, restart changed ones.
- API: `GET /api/config`, `PUT /api/config` (admin, CSRF-protected),
  `POST /api/admin/restart` for fields that genuinely need a process
  restart (e.g. `listen_addr`). Restart uses `os.Executable()` +
  `syscall.Exec` on Linux; under Docker / systemd the orchestrator
  brings the process back. Both paths documented.
- UI: schema-driven form; fields that require restart are marked, and
  a "Restart now" button is shown when any are pending.

## Phase 4 — Directory watcher

Goal: replace bespoke per-file configuration with a `type: "dir"`
source that handles rotation correctly.

- Recursive directory scan + glob filter (e.g. `access*.log*`).
- `file_state(path, inode, size, offset, mtime)` table tracks per-file
  position so the watcher survives restart and detects rotation by
  inode change.
- One-shot import of rotated `.gz` (and eventually `.bz2` / `.xz`)
  files; live tailing for the active file.
- Integrates with the settings panel (Phase 3): directory sources are
  configured from the UI like any other source.

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
