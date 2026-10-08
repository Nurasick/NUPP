# Backlog

Known, accepted minor issues from code reviews. Each was judged low-impact and deferred;
pick them up when touching the related code.

## From the Plan 1 review (2026-10-07)

- **Shutdown:** if graceful shutdown times out, connections were not force-closed. *(Fixed in Plan 1.5.)*
- **Storage:** no fsync before rename. *(Fixed in Plan 1.5.)*
- **Search:** `SearchCourses` and `CountCourses` run in separate snapshots, so `meta.total` can briefly
  disagree with the page under concurrent writes. A `count(*) OVER ()` window would fix it.

## From the Plan 1.5 review (2026-10-08)

- **Rate limiter eviction** assumes `burst / rate ≤ 60 s`. With unusual settings (e.g. 0.1 req/s, burst 100)
  an idle client gets a fresh burst after 61 s. Fix: validate the ratio, or evict only full buckets.
- **Non-`/api` 404s** (`/`, `/favicon.ico`) are Go's plain-text 404 without `Cache-Control`. Production routes
  those paths to the web app, not the API. Fix: a catch-all `"/"` using `httpx.Fail`.
- **Cancelled requests are logged as `status: 200`**. Fix: record 499 or a `cancelled` field.
- **Client disconnects while writing JSON** are logged at ERROR. Fix: log network errors at info/debug.
- **Comments:** `apiWriteTimeout`'s 15 s starts before the handler runs; the `deadlines` doc comment
  mislabels `/healthz`'s ping timeout as a write timeout.
- **`/healthz` during a DB outage** logs an ERROR on every call (including cached results), and callers
  queue up to 2 s behind a ping. Fix: log only fresh failures.
- **`-healthcheck`** always probes `127.0.0.1`, so it fails for `HTTP_ADDR=[::1]:8080` or a specific
  interface address.
- **IPv4-mapped CIDRs** in `TRUSTED_PROXIES` (`::ffff:172.30.0.0/120`) never match. Fix: unmap or reject.
- **Campus NAT:** students behind one IPv4 address share 8 parallel downloads and 30 file req/s. Plan 7
  should measure and raise the limits or allow-list the campus egress address.
