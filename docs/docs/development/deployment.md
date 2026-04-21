# Deployment & Operations

## Docker Compose Architecture

Memoh uses a dual compose file pattern:

| File | Purpose |
|------|---------|
| `docker-compose.yml` | Production config — references pre-built images |
| `docker/docker-compose.yml` | Build configs — defines how to build images locally |

When deploying local changes, you must use **both** files.

## Build & Deploy Commands

### Server (Go Backend)

```bash
# Build server image
docker compose -f docker-compose.yml -f docker/docker-compose.yml build server

# Deploy (recreate containers)
docker compose -f docker-compose.yml -f docker/docker-compose.yml up -d --force-recreate server migrate

# Or shorter, if docker/docker-compose.yml is in COMPOSE_FILE env:
docker compose build server
docker compose up -d --force-recreate server migrate
```

### Web (Vue Frontend)

**IMPORTANT**: Frontend changes require rebuilding the Docker image. Simply restarting the container is NOT enough.

```bash
# Build web image
docker compose -f docker-compose.yml -f docker/docker-compose.yml build web

# Deploy
docker compose -f docker-compose.yml -f docker/docker-compose.yml up -d --force-recreate web
```

### Browser Gateway (Optional)

```bash
docker compose -f docker-compose.yml -f docker/docker-compose.yml build browser
docker compose -f docker-compose.yml -f docker/docker-compose.yml up -d --force-recreate browser
```

### Full Redeploy (All Services)

```bash
docker compose -f docker-compose.yml -f docker/docker-compose.yml build
docker compose -f docker-compose.yml -f docker/docker-compose.yml up -d --force-recreate
```

## Development Environment

```bash
# Start dev environment (all services)
mise run dev

# Start with minified services
mise run dev:minify

# Stop
mise run dev:down

# View logs
mise run dev:logs

# Restart a specific service
mise run dev:restart -- server
```

Dev URLs:
- Web UI: `http://localhost:18082`
- Server: `http://localhost:18080`
- Browser Gateway: `http://localhost:18083`

## Git Configuration

### Standard Remotes

```bash
origin   → git@github.com:kasuganosora/Memoh.git    # User's fork
upstream → git@github.com:memohai/Memoh.git          # Official repo
```

### Branch Conventions

- `main` — Production branch
- `agent-opus` — Feature development branch (pushed to origin)
- `feat/*` — Feature branches

### Common Git Operations

```bash
# Pull upstream changes
git fetch upstream
git merge upstream/main

# Push to fork
git push origin main

# Create PR to upstream
gh pr create --repo memohai/Memoh --head kasuganosora:main --base main
```

## Health Checks

```bash
# Server health
curl -s http://localhost:8080/health
# Expected: 200 OK

# Docker container health
docker compose ps

# Database connectivity
docker compose exec postgres psql -U memoh -d memoh -c "SELECT 1"
```

## Log Viewing

```bash
# Server logs (recent)
docker logs memoh-server --tail 50 --since 10m

# Follow in real-time
docker logs memoh-server -f

# Filter errors
docker compose logs server --since 30m 2>&1 | grep -iE "ERROR|panic|fatal"

# Filter for specific bot
docker compose logs server --since 10m 2>&1 | grep "BOT_UUID"

# All services
docker compose logs --tail 20 --since 5m
```

## Database Operations

```bash
# Run migrations
mise run db-up

# Drop and recreate database
mise run db-down

# Run SQL directly
docker compose exec postgres psql -U memoh -d memoh -c "SQL_QUERY"

# Check migration status
docker compose exec postgres psql -U memoh -d memoh -c "SELECT * FROM schema_migrations ORDER BY version DESC LIMIT 5"
```

## Code Quality

```bash
# Go linting
golangci-lint run ./...

# Go linting with auto-fix
golangci-lint run --fix ./...

# Lint specific package
golangci-lint run ./internal/channel/adapters/misskey/...

# Frontend linting
cd apps/web && pnpm lint

# Frontend linting with fix
cd apps/web && pnpm lint:fix

# Go formatting
go fmt ./...

# Run all linters (Go + frontend)
mise run lint

# Run all linters with auto-fix
mise run lint:fix
```

## Common Issues

### gci Import Ordering

Pre-commit hooks check import ordering. If commits fail:

```bash
golangci-lint run --fix ./internal/channel/adapters/misskey/...
```

### Web UI Not Updating

The web container serves a pre-built static bundle. You must rebuild the Docker image:

```bash
docker compose build web
docker compose up -d --force-recreate web
```

### Server Not Picking Up Changes

Make sure to rebuild and recreate:

```bash
docker compose build server
docker compose up -d --force-recreate server
```

Just `docker compose restart server` uses the old image.

### Database Migration Fails

Check if the migration is idempotent (uses `IF NOT EXISTS`). Check the error:

```bash
docker compose logs migrate --tail 20
```

### Container Record Missing

Bot container records can become stale. Check:

```bash
docker compose exec postgres psql -U memoh -d memoh -c \
  "SELECT id, bot_id, status FROM containers WHERE bot_id = 'BOT_UUID'"
```
