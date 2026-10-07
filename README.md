# NUPP

An open, community-driven archive of university learning materials — past midterms,
quizzes, finals, notes and slides — organised by **course → year/term → assessment**.

Anyone can browse. Anyone signed in can suggest new materials. Every submission goes
through **moderation** before it becomes public.

## Status

🚧 Plan 1 of 7 done: the read-only catalog API. Next: the web UI.
Design: [`docs/SYSTEM_DESIGN.md`](docs/SYSTEM_DESIGN.md) ·
API spec: [`docs/superpowers/specs/`](docs/superpowers/specs/) ·
API contract: [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml)

## Stack

Go API (stdlib `net/http`, pgx, sqlc, goose) · PostgreSQL 17 · Next.js frontend (coming) ·
Caddy · Docker Compose · self-hosted.

## Development

Requirements: **Go 1.26.6+** and **Docker Desktop** (running).

```bash
cp .env.example .env              # set POSTGRES_PASSWORD (URL-safe characters)
cp api/.env.example api/.env      # put the same password into DATABASE_URL
docker compose up -d postgres     # dev database on localhost:5433

cd api
go run ./cmd/seed                 # demo data (safe to run repeatedly)
go run ./cmd/server               # http://localhost:8080/healthz
go test ./...                     # starts throwaway Postgres containers
```

Try it:

```bash
curl "localhost:8080/api/v1/courses?q=csci"
curl localhost:8080/api/v1/courses/csci-151
```

Or run everything in containers: `docker compose up -d --build`, then
`docker compose exec api /app/seed`. (Containers keep files in a Docker volume,
separate from `api/data/` used by `go run`, so seed each setup separately.)

**After editing `api/internal/catalog/queries.sql` or a migration**, regenerate the Go code
(from `api/`, PowerShell syntax; in bash use `"$PWD:/src"`):

```powershell
docker run --rm -v "${PWD}:/src" -w /src sqlc/sqlc:1.29.0 generate
```

**The API contract** is `api/openapi/openapi.yaml`. `go test` fails if any JSON response
stops matching it, so change both together.

### Configuration

All settings are environment variables (see `api/.env.example`). Besides the basics
(`DATABASE_URL`, `HTTP_ADDR`, `STORAGE_DIR`, `APP_ENV`):

| Variable | Default | Purpose |
|----------|---------|---------|
| `TRUSTED_PROXIES` | *(empty)* | CIDRs whose `X-Forwarded-For` is believed (set behind Caddy) |
| `RATE_LIMIT_ENABLED` | `true` | Turn per-client rate limiting off for local load tests |
| `RATE_LIMIT_API_RPS` / `_BURST` | `20` / `100` | JSON requests per second / burst, per client |
| `RATE_LIMIT_FILES_RPS` / `_BURST` | `30` / `300` | File requests per second / burst, per client |
| `MAX_INFLIGHT_API` / `MAX_INFLIGHT_FILES` | `32` / `64` | Concurrent requests before answering 503 |
| `MAX_INFLIGHT_FILES_PER_CLIENT` | `8` | Concurrent downloads per client before 429 |
| `DB_MAX_CONNS` | `10` | Serving connection pool size |

### Security & limits

What protects the API (details: [hardening spec](docs/superpowers/specs/2026-10-07-plan-1.5-hardening-spec.md)):

- **Rate limiting** per client IP (IPv6 per /64): `429` + `Retry-After`.
- **Concurrency caps**: `503 overloaded` instead of queueing; max 8 parallel downloads per client.
- **Timeouts**: 5 s per SQL statement, 10 s per JSON request, header/read/write deadlines
  against slow clients (downloads get `30 s + size ÷ 32 KiB/s`).
- **Headers**: `nosniff`, `frame-ancestors 'none'` CSP (sandbox CSP on files), CORP `same-origin`,
  `no-store` on every error.
- **Containers**: read-only filesystem, no Linux capabilities, memory/CPU/PID limits, rotated logs.
- **Supply chain**: CI runs `govulncheck` + `gosec` (and weekly), actions pinned by SHA, Dependabot.

### Code map

| Path | What lives there |
|------|------------------|
| `api/cmd/server` | Entrypoint: config → DB → migrations → HTTP server |
| `api/cmd/seed` | Demo data for local development |
| `api/internal/db` | Connection pool + SQL migrations (embedded in the binary) |
| `api/internal/catalog` | Public catalog: SQL queries, handlers, JSON views |
| `api/internal/httpx` | JSON envelope, error codes, pagination |
| `api/internal/storage` | File storage interface + local-disk implementation |
| `api/internal/server` | Routing, middleware (limits, deadlines, headers), health check |
| `api/internal/clientip` | Real client IP behind trusted proxies |
| `api/internal/ratelimit` | Per-client token buckets |
| `api/internal/testutil` | Test helpers: throwaway Postgres, fixtures |

## Contributing materials

Once the site is live: sign in, click **Suggest material**, pick the course, year and
assessment, attach PDFs/images, and submit. A moderator will review it.

Please only upload materials you are allowed to share. Copyright holders can request
removal via the report button on any material.

## License

Code: [MIT](LICENSE). Uploaded materials remain the property of their respective authors.
