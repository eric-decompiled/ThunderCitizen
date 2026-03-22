# Deploy to DigitalOcean App Platform

ThunderCitizen runs on **DigitalOcean App Platform** (hosted containers, managed TLS) backed by **DigitalOcean Managed Postgres**. Container images are built by GitHub Actions (`.github/workflows/build-image.yml`) and published to GHCR; App Platform pulls the `:latest` tag.

Why this setup: DO owns the TLS cert, DO owns database backups, and the app image is built once in CI and deployed as-is — no droplet, no ssh, no reverse proxy.

## Prerequisites

- [`doctl`](https://docs.digitalocean.com/reference/doctl/how-to/install/) authenticated against your DO team (`doctl auth init`)
- `psql` on your workstation (to enable extensions and apply patches once)
- DNS control for `thundercitizen.ca` (to point it at the App Platform app)
- The GHCR package `ghcr.io/<owner>/thundercitizen` set to **public** visibility (Package settings → Change visibility → Public). Otherwise App Platform needs a registry pull secret.

## 1. Managed Postgres (one-time)

Create the database via dashboard (**Databases → Create → PostgreSQL 16**) or:

```bash
doctl databases create thundercitizen-db \
    --engine pg --version 16 \
    --region tor1 --size db-s-1vcpu-1gb --num-nodes 1
```

Tor1 (Toronto) keeps latency low for Thunder Bay readers. `db-s-1vcpu-1gb` is the smallest Managed Postgres tier — check DO's pricing page for current rates before you commit.

Once the database is provisioned, grab its connection string:

```bash
doctl databases connection thundercitizen-db --format URI
```

Then enable the two extensions `migrations/000001_schema.up.sql` expects (they ship with DO's PostGIS-enabled image but must be activated per database):

```bash
psql "<connection-uri>" -c 'CREATE EXTENSION IF NOT EXISTS postgis; CREATE EXTENSION IF NOT EXISTS pg_trgm;'
```

Your workstation's IP must be on the database's **Trusted Sources** allow-list for this to work — add it in the dashboard (**Databases → thundercitizen-db → Settings → Trusted Sources**) or via `doctl databases firewalls append`.

## 2. App Platform app (one-time)

Create the app from the dashboard: **Apps → Create App → Container Image → GitHub Container Registry**.

Fill in:

| Field | Value |
|---|---|
| Registry | `ghcr.io` |
| Repository | `<owner>/thundercitizen` |
| Tag | `latest` |
| HTTP port | `8080` |
| Instance size | Basic XXS (512 MB) or Basic XS (1 GB) |
| Instance count | 1 |
| Health check path | `/health` |
| Region | `tor1` (same as the DB) |

Attach the Managed Postgres database as a component of the app — in the dashboard: **Add resources → Database → Existing Database → thundercitizen-db**. Once attached, App Platform makes a binding reference available as `${thundercitizen-db.DATABASE_URL}` that you can wire into the app's env vars.

### Environment variables

Set these on the **app component** (not the database component):

| Variable | Scope | Value |
|---|---|---|
| `DATABASE_URL` | RUN_TIME | `${thundercitizen-db.DATABASE_URL}` (binding reference, not a literal) |
| `ENVIRONMENT`  | RUN_TIME | `production` |
| `PORT`         | RUN_TIME | `8080` |

`internal/config` reads env vars first and falls back to `secrets.conf` if present. The runtime image does not bake in `secrets.conf`, so the env-var values above are what the server sees.

**Do not set `ANTHROPIC_API_KEY` on the running app** — it is only needed by `cmd/summarize`, which is an operator tool, not part of the web service. Keep it in your local `secrets.conf`.

### Custom domain

In the app's **Domains** tab, add `thundercitizen.ca` and (optionally) `www.thundercitizen.ca`. DO gives you a CNAME target like `<app-name>.ondigitalocean.app`. Point your DNS at it. The Let's Encrypt cert is provisioned automatically once DNS resolves — usually within a minute or two of propagation.

## 3. PMTiles basemap

`static/thunderbay.pmtiles` is committed to the repo (~8 MB — a Thunder Bay bbox extract of the Protomaps global basemap) and copied into the image by the existing `COPY --from=builder /src/static /app/static` line in the Dockerfile. No extra build step needed.

Regenerate it locally when Protomaps ships a new global snapshot (infrequent):

```bash
pmtiles extract https://build.protomaps.com/<YYYYMMDD>.pmtiles \
    static/thunderbay.pmtiles \
    --bbox=-89.85,48.10,-88.75,48.65 --maxzoom=15
git add static/thunderbay.pmtiles && git commit -m "Refresh basemap snapshot"
```

## 4. Data storage (DO Spaces)

Data patches (`patches/*.sql`) are served from `data.thundercitizen.ca` (a DO Spaces bucket with CDN). The server downloads `patches.zip` on boot and applies any new patches — no manual DB step needed.

### One-time setup

Create the Space via dashboard (**Spaces → Create → `thundercitizen-data`**, region `tor1`) or:

```bash
doctl serverless install   # if needed
# Spaces are created via the dashboard — doctl doesn't have a create command yet.
```

1. **Enable CDN** on the Space (Settings → CDN → Enable)
2. **Custom subdomain**: add `data.thundercitizen.ca` as a custom CDN endpoint. DO provisions a Let's Encrypt cert automatically.
3. **DNS**: CNAME `data.thundercitizen.ca` → the CDN endpoint DO gives you (e.g. `thundercitizen-data.tor1.cdn.digitaloceanspaces.com`)

### Upload patches

After updating patch SQL files locally:

```bash
./scripts/apply.sh              # zip + upload to DO Spaces
./scripts/apply.sh --dry-run    # preview only
```

Requires `s3cmd` or `aws` CLI configured for DO Spaces. The next server boot (or redeploy) picks up the new zip automatically. Idempotent — already-applied patches are a no-op.

### Environment variables

The server defaults to `https://data.thundercitizen.ca/patches.zip` in production — no env var needed unless you want to override:

| Variable | Scope | Default | Notes |
|---|---|---|---|
| `PATCHES_URL` | RUN_TIME | `https://data.thundercitizen.ca/patches.zip` | Override to point at a different zip |

## 5. Verify

```bash
# App health
curl -I https://thundercitizen.ca/health

# Version + commit (should match the GHA image tag for the deployed release)
curl -s https://thundercitizen.ca/version

# Live logs
doctl apps logs <app-id> --follow

# App + deployment status
doctl apps get <app-id>
```

Find `<app-id>` with `doctl apps list`.

## Updating

1. Merge to `main` — GitHub Actions builds a new image and pushes it to GHCR as `:latest`, `:main`, and `:sha-<short>`.
2. App Platform pulls the new `:latest` automatically if **Auto-Deploy** is enabled on the component (dashboard → Settings → Auto Deploy). Otherwise trigger manually:

   ```bash
   doctl apps create-deployment <app-id>
   ```

Schema migrations run on server startup (`cmd/server/main.go:runMigrations`) against the Managed DB. No manual step.

If you changed any patch SQL, run `./scripts/apply.sh` to upload the new zip. The next server boot picks it up automatically.

## Rollback

Roll back to a previous deployment via the dashboard (**Apps → thundercitizen → Deployments → … → Rollback**) or:

```bash
doctl apps list-deployments <app-id>
doctl apps create-deployment <app-id> --force-rebuild  # to rebuild from a specific tag
```

For a fast rollback to a known-good image, override the component's image tag to a specific `:sha-<short>` from the GHA history instead of `:latest`, then redeploy.

**Schema migrations are forward-only.** Rolling back the app image does NOT roll back a schema migration that was already applied on startup. If a deploy includes a destructive migration, plan the rollback path before merging.

## Backups

DO Managed Postgres takes automated backups on every tier (daily full + transaction logs for point-in-time recovery). Inspect and restore from the dashboard (**Databases → thundercitizen-db → Backups**) or via doctl:

```bash
doctl databases backups list thundercitizen-db
# Restore via the dashboard — doctl can list backups but not restore into place;
# restores always create a new cluster that you then swap the app binding to.
```

`scripts/backup.sh --dev` runs `pg_dump` via `docker exec` against the containerized dev DB. Don't use it for production; DO's snapshots are the production backup story. (`scripts/backup.sh` without `--dev` targets the prod compose stack; see DEPLOY-DROPLET.md.)

## Monitoring

- **Logs** — `doctl apps logs <app-id> --follow` or the **Runtime Logs** tab in the dashboard
- **Metrics** — App Platform dashboard shows request rate, latency, memory, CPU
- **Health** — `/health` returns 200 when the DB pool is up
- **Version** — `/version` returns `{commit, build_time}` from the GHA-built image's ldflags

## Local testing

`docker compose up` still works for local integration testing against the containerized Postgres — see `docker-compose.yml`. It has nothing to do with production; the compose file is a dev convenience only.
