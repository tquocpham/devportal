# Phase 5: React + TypeScript frontend, served alongside the current one

## Context

The current frontend (`cmd/api/web/`: `index.html`, `style.css`, `app.js`) is vanilla JS with zero dependencies and no build step, `//go:embed`ded straight into the `cmd/api` binary. The user wants to eventually move to TypeScript + React, but explicitly **not** as a big-bang rewrite: build the new app alongside the old one, serve both from the same backend on different paths, and port features over incrementally, retiring the old app only once the new one has real parity. This also directly motivated the original ask (real UI routing) — React Router is the natural way to get that in the new app, rather than hand-rolling `pushState`/`popstate` into the current vanilla app just to throw it away later.

This plan covers **standing up the new app and proving the whole pipeline works end-to-end with one simple view**, not porting every feature, that's deliberately left for follow-up work once this foundation is in place.

## Approach

**New `frontend/` directory at the repo root**, a third kind of module alongside the two Go binaries (`cmd/api`, `cmd/indexer`) and `pkg/retrieval`, matching how this repo already separates concerns by top-level directory. Vite + React + TypeScript + React Router: Vite because this is an internal, auth-gated SPA with no SEO/SSR need, Next.js would be meaningfully more machinery than this calls for. React Router as the standard, well-known choice for client-side routing, no reason to reach for something more exotic here.

**Served from the same Go backend, not a separate Node server in production.** Same pattern the current app already uses, just a second instance of it:
```go
//go:embed frontend/dist
var nextFS embed.FS
```
mounted at a **new path prefix** (`/next`, easy to change later) alongside the existing root mount, `webAssets := echo.MustSubFS(webFS, "web"); e.StaticFS("/", webAssets)` stays completely untouched. This means the React app's `dist/` has to exist *before* `go build` runs, so the Dockerfile needs a new Node build stage ahead of the existing Go build stage, and local dev needs `npm run build` (in `frontend/`) run before `go build`/`go run` picks up a fresh embed.

**SPA fallback route is required, not optional**, confirmed directly against the installed Echo source (`context_fs.go`'s `fsFile`): it calls `filesystem.Open(file)` first and returns `ErrNotFound` immediately if that exact path doesn't exist in the embedded FS. The `index.html` fallback only fires when the opened path *is* a real directory (`fi.IsDir()`), it never fires for an arbitrary missing path. That means `StaticFS` alone will correctly serve `/next/` (a real directory) but will 404 on `/next/admin/users` (a client-side React Router route with no matching file), exactly the "SPA route 404s on refresh" problem. Needs an explicit catch-all under `/next/*` that serves `frontend/dist/index.html` for anything that isn't a real static asset (JS/CSS/image file), registered so it only catches the miss case, not shadowing real asset requests.

**No backend duplication.** Both the old and new frontends call the exact same `/api/v1/...` routes, nothing in `cmd/api/lib/handlers` changes. Since both are served from the same origin, the existing `session` cookie is automatically shared, logging in via the old app at `/` also authenticates the new app at `/next` for free, no parallel auth path to build.

**Prove the pipeline with one view before porting everything.** Recommend starting with the Home/profile view: it's the simplest (mostly read-only display of `/api/v1/me` + `/api/v1/repos`), and getting it right end-to-end (build → embed → serve → shared-session auth → routing) validates the whole foundation before sinking time into porting the more complex views (Admin's grant/revoke UI, the AWS flows' multi-step reveal boxes).

**Explicitly out of scope for this plan**: porting every view, retiring the old app, and moving `/next` to `/` once there's parity, that's real follow-up work once this foundation is proven, not part of standing it up.

## Files

- `frontend/` (new) — `package.json`, `vite.config.ts` (with `base: '/next/'` so asset paths are correct under the mount prefix, and easy to change to `/` later at retirement time), `tsconfig.json`, `src/main.tsx`, `src/App.tsx`, initial route(s) for the one proof-of-concept view
- `cmd/api/main.go` — second `//go:embed frontend/dist`, second `StaticFS` mount at `/next`, the SPA-fallback catch-all route
- `cmd/api/Dockerfile` — new Node build stage (`node:XX-alpine`, `npm ci && npm run build` in `frontend/`) ahead of the existing Go build stage, `COPY --from=frontend-build /src/frontend/dist ./frontend/dist` into the Go build stage so the embed directive finds it
- `.gitignore` — `frontend/node_modules/`, `frontend/dist/`

## Verification

- `npm run build` in `frontend/` produces a real `dist/`, `go build ./cmd/api` succeeds with it embedded.
- `docker compose up -d --build api` (full pipeline, matching how this project already rebuilds/tests everything else): confirm `/` still serves the old app exactly as before, `/next` serves the new one.
- Log in via the old app at `/`, then load `/next` without logging in again, confirm `/api/v1/me` succeeds there too, proving the shared-session property actually holds, not just assumed from same-origin reasoning.
- Directly load (or refresh) a nested client-side route under `/next` (not just `/next/` itself), confirm it renders the app instead of a raw 404, the specific failure mode identified from Echo's source.
- Confirm `/` and every existing route under it (`/auth/github`, `/api/v1/*`, `/mcp`) are completely unaffected, this should be additive only.
