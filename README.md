# NUPP

An open, community-driven archive of university learning materials — past midterms,
quizzes, finals, notes and slides — organised by **course → year/term → assessment**.

Anyone can browse. Anyone signed in can suggest new materials. Every submission goes
through **moderation** before it becomes public.

## Status

🚧 Early design phase. See [`docs/SYSTEM_DESIGN.md`](docs/SYSTEM_DESIGN.md).

## Planned stack

- **Next.js** (TypeScript) on **Vercel**
- **Supabase** — Postgres, Auth, Row Level Security
- **Cloudflare R2** — file storage (free egress)
- **PDF.js** for in-browser previews

## Contributing materials

Once the site is live: sign in, click **Suggest material**, pick the course, year and
assessment, attach PDFs/images, and submit. A moderator will review it.

Please only upload materials you are allowed to share. Copyright holders can request
removal via the report button on any material.

## License

Code: [MIT](LICENSE). Uploaded materials remain the property of their respective authors.
