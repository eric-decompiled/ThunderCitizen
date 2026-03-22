# Docker

## Commands

```bash
make build   # Build Docker image
make up      # Start Postgres + app (foreground)
make up-d    # Start in background
make down    # Stop services
make clean   # Stop and remove volumes
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| `db` | 5432 | PostgreSQL 16 |
| `app` | 8080 | Go application |

## Database Only

To run just Postgres for local development:

```bash
docker compose up db
```

Then use `make dev` to run the Go app locally with hot reload.
