# Development TODO

Tracks small deferred items. The phased plan (Phases 1–5) lives in
[`ROADMAP.md`](./ROADMAP.md); this file collects sub-tasks that don't
own a full phase.

## Parser

- [ ] **Apache `%`-style LogFormat tokens** in the `custom` engine
      (e.g. `%h %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-Agent}i\"`).
      Today only Nginx-style `$variable` tokens are accepted.
- [ ] Expand the Nginx variable set: `$request_length`, `$ssl_protocol`,
      `$ssl_cipher`, `$gzip_ratio`, etc.
- [ ] Smarter auto-detection that also tries `apache:common` (currently auto
      only tries Nginx vhost/combined and Apache combined).

## Compressed log support

- [x] gzip (`.gz`) — supported via `read_compressed: true` on file sources and
      auto-detected when the `-import` path ends with `.gz`.
- [ ] bzip2 (`.bz2`) — standard library has a reader; not yet wired in.
- [ ] xz (`.xz`)   — requires a third-party package (e.g.
      `github.com/ulikunitz/xz`); pending dependency review.

## Directory / glob sources

- [ ] A `type: "dir"` source that recursively scans a directory for files
      matching a glob (e.g. `access*.log*`), tracks `(inode, offset)` in a
      `file_state` table to survive rotation, and integrates with the live
      tailer.

## Apache integration (beyond log reading)

The current Apache support is log-file based only. A future iteration could:

- [ ] Document `mod_log_config` snippets for streaming via syslog/`piped logs`.
- [ ] Provide canned dashboards or filters keyed on Apache-specific fields.

## Misc

- [ ] CLI helper: `web-req-attr validate-config` to surface validation errors
      without starting the server.
- [ ] CLI helper: `web-req-attr test-pattern <pattern> <line>` for iterating on
      custom log patterns.
