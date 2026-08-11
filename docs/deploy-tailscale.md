# Deploying on spare hardware + Tailscale

Runs the whole stack (`api` + `postgres`) via `docker-compose.yml` on an old machine sitting on your network, reachable only by your team over a private Tailscale network — no cloud hosting, no public exposure, no Kubernetes.

Tailscale already encrypts traffic point-to-point (WireGuard) between every device on your tailnet, so there's no need for HTTPS termination on top of it — plain HTTP over the tailnet is already private. One real implication: browsers only expose `navigator.clipboard` (used by the admin panel's "Copy" buttons) on secure contexts, which a plain `http://` origin doesn't count as — the UI already falls back to `document.execCommand('copy')` for this, so it still works, just via the older API.

## One-time machine setup

1. **Docker + Docker Compose** installed on the old machine.
2. **[Tailscale](https://tailscale.com/download)** installed and signed into your team's tailnet. Enable **MagicDNS** (Tailscale admin console → DNS) so the machine gets a stable hostname like `old-macbook.tailXXXXX.ts.net` — prefer this over the raw `100.x.y.z` IP, since the IP can change but the MagicDNS name won't.
3. **Keep it awake.** A closed laptop lid or sleep settings will take the whole team's access down.
   - macOS: System Settings → Energy → disable sleep when on power adapter, or run `caffeinate -s` in a persistent session. Closing the lid still sleeps the machine unless you also keep an external display attached — simplest is to just leave it open, or dig into `pmset` if you want it fully headless.
   - Old PC: enable "restore power after outage" in BIOS, disable sleep in OS power settings.

## Repo setup on the deployment machine

Clone the repo there (or `git pull` an existing checkout), then create `cmd/api/config.local.yaml` from `cmd/api/config.local.example.yaml` — **this is a separate file from the one on your dev machine**, tailored to this environment:

```yaml
database_url: postgres://postgres:postgres@postgres:5432/indexer?sslmode=disable
```

`postgres` here is the compose service name, not `localhost` — containers on the same compose network reach each other by service name, not by the host-mapped port (`5433`, which only matters from *outside* the compose network, e.g. when you run `migrate.sh` below).

```yaml
callback_url: http://old-macbook.tailXXXXX.ts.net:3000/auth/callback
```

Use your actual MagicDNS hostname. Update your GitHub OAuth App's callback URL to match exactly (Settings → Developer settings → OAuth Apps) — a mismatch here is the most common thing that breaks login.

Fill in the rest of the keys the same way you did locally (`anthropic_api_key`, `voyage_api_key`, `jwt_secret`, etc.).

## Database setup (run once, from the deployment machine's host — not inside a container)

Start Postgres first so `migrate.sh` has something to connect to:

```bash
docker compose up -d postgres
```

Then, same as local dev — note this uses `localhost:5433`, the host-mapped port, since these run directly on the host:

```bash
DATABASE_URL="postgres://postgres:postgres@localhost:5433/indexer?sslmode=disable" cmd/api/migrations/migrate.sh
psql "postgres://postgres:postgres@localhost:5433/indexer?sslmode=disable" -f cmd/api/migrations/seed/seed-admin.sql
```

Then index the codebase once (see [`cmd/indexer/README.md`](../cmd/indexer/README.md)) — also run from the host against `localhost:5433`.

## Start the service

```bash
docker compose up -d --build
```

`api`'s `depends_on: postgres: condition: service_healthy` means it won't start until Postgres is actually accepting connections. Both services have `restart: unless-stopped`, so a machine reboot (power blip on old hardware — plan for this) brings the whole stack back automatically, no manual restart needed.

## Verifying it worked

From any device on the same tailnet:

```
http://old-macbook.tailXXXXX.ts.net:3000
```

Should show the login page. Log in, confirm the chat works and citations look right. `docker compose logs -f api` for anything that goes wrong.
