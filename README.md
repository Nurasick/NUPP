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

Requirements: **Go 1.26+** and **Docker Desktop** (running).

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

### Code map

| Path | What lives there |
|------|------------------|
| `api/cmd/server` | Entrypoint: config → DB → migrations → HTTP server |
| `api/cmd/seed` | Demo data for local development |
| `api/internal/db` | Connection pool + SQL migrations (embedded in the binary) |
| `api/internal/catalog` | Public catalog: SQL queries, handlers, JSON views |
| `api/internal/httpx` | JSON envelope, error codes, pagination |
| `api/internal/storage` | File storage interface + local-disk implementation |
| `api/internal/server` | Routing, middleware, health check |
| `api/internal/testutil` | Test helpers: throwaway Postgres, fixtures |

## Contributing materials

Once the site is live: sign in, click **Suggest material**, pick the course, year and
assessment, attach PDFs/images, and submit. A moderator will review it.

Please only upload materials you are allowed to share. Copyright holders can request
removal via the report button on any material.

## License

Code: [MIT](LICENSE). Uploaded materials remain the property of their respective authors.
