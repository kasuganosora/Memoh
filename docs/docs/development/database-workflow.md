# Database Workflow

## Overview

Memoh uses PostgreSQL with [sqlc](https://sqlc.dev/) for type-safe Go code generation. SQL queries are defined in `db/queries/`, and Go code is auto-generated into `internal/db/sqlc/`.

**Rule: Never manually edit files under `internal/db/sqlc/`.**

## Migration System

### File Location

```
db/migrations/
├── 0001_init.up.sql          # ← Canonical full schema (ALWAYS up-to-date)
├── 0001_init.down.sql
├── 0002_xxx.up.sql           # ← Incremental migration (delta only)
├── 0002_xxx.down.sql
├── 0003_yyy.up.sql
└── 0003_yyy.down.sql
```

### Dual-Update Convention

Every schema change requires **two updates**:

1. **Update `0001_init.up.sql`** — This is the canonical full schema. It must always contain the complete, up-to-date database definition (all tables, indexes, constraints). Add your new column/table here.

2. **Create an incremental migration** — A new file `XXXX_description.up.sql` containing only the delta needed to upgrade an existing database.

### Naming Convention

```
{NNNN}_{snake_case_description}.up.sql
{NNNN}_{snake_case_description}.down.sql
```

- `{NNNN}` is zero-padded sequential number
- Always create both `.up.sql` and `.down.sql`

### Header Comment

Each migration should start with:

```sql
-- 0005_add_feature_x
-- Add feature_x column to bots table for ...
```

### Idempotent DDL

Use `IF NOT EXISTS` / `IF EXISTS` guards so migrations are safe to re-run:

```sql
CREATE TABLE IF NOT EXISTS ...;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS feature_x TEXT;
DROP TABLE IF EXISTS old_table;
```

### Down Migration Rules

The `.down.sql` must cleanly undo everything the `.up.sql` does, in reverse order:

```sql
-- 0005_add_feature_x (down)
ALTER TABLE bots DROP COLUMN IF EXISTS feature_x;
```

## sqlc Query Definitions

### File Location

One `.sql` file per domain in `db/queries/`:

```
db/queries/
├── users.sql
├── bots.sql
├── channels.sql
├── sessions.sql
├── messages.sql
├── providers.sql
├── models.sql
├── ...
```

### Query Annotations

```sql
-- name: CreateUser :one
-- Returns a single row
INSERT INTO users (username, email, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListAccounts :many
-- Returns a slice of rows
SELECT * FROM users
WHERE is_active = true
ORDER BY created_at DESC;

-- name: UpdateBotSettings :one
-- With RETURNING * to get the updated row
UPDATE bots
SET settings = $2
WHERE id = $1
RETURNING *;

-- name: DeleteSession :exec
-- No return value
DELETE FROM bot_sessions WHERE id = $1;

-- name: SearchMessages :many
-- With sqlc.arg() for complex types and LIMIT
SELECT * FROM bot_history_messages
WHERE session_id = sqlc.arg('session_id')::uuid
  AND created_at > sqlc.arg('after')::timestamptz
ORDER BY created_at ASC
LIMIT sqlc.arg('limit')::int;
```

### Annotation Types

| Suffix | Go return type | Use when |
|--------|---------------|---------|
| `:one` | `(Row, error)` | Query returns exactly one row |
| `:many` | `([]Row, error)` | Query returns zero or more rows |
| `:exec` | `error` | Query returns nothing (INSERT/UPDATE/DELETE without RETURNING) |
| `:execrows` | `(int64, error)` | Need to know affected row count |

### Nullable Fields

PostgreSQL nullable columns generate `pgtype.X` types in Go:

```sql
-- If password_hash is nullable (TEXT NULL):
-- Go type: pgtype.Text (with .String and .Valid fields)

-- If display_name has a default:
-- Go type: pgtype.Text
```

Always check `.Valid` before using nullable fields:

```go
if row.DisplayName.Valid {
    name = row.DisplayName.String
}
```

### JSON/JSONB Columns

JSONB columns are generated as `pgtype.JSONB` or `[]byte`. Use `json.Unmarshal` to parse:

```go
var routing map[string]any
if row.Routing.Valid {
    json.Unmarshal(row.Routing.Bytes, &routing)
}
```

## Complete Workflow

### Adding a new column

```bash
# 1. Update 0001_init.up.sql (add column to the table definition)
# 2. Create incremental migration
vim db/migrations/0073_add_my_column.up.sql
vim db/migrations/0073_add_my_column.down.sql

# 3. If adding a query, edit the relevant query file
vim db/queries/bots.sql

# 4. Regenerate Go code
mise run sqlc-generate

# 5. Apply migration
mise run db-up

# 6. Build and verify
go build ./...
```

### Adding a new table

```bash
# 1. Update 0001_init.up.sql (add CREATE TABLE)
# 2. Create incremental migration with CREATE TABLE IF NOT EXISTS
# 3. Create a new query file: db/queries/my_table.sql
# 4. Run sqlc-generate and db-up
```

### Modifying an existing query

```bash
# 1. Edit db/queries/xxx.sql
# 2. Run mise run sqlc-generate
# 3. Update calling code to match new generated signatures
# 4. Build and test
```

## sqlc Configuration

`sqlc.yaml` key settings:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "db/queries"
    schema: "db/migrations"
    gen:
      go:
        package: "sqlc"
        out: "internal/db/sqlc"
        sql_package: "pgx/v5"
        emit_json_tags: true
        overrides:
          - db_type: "user_role"
            go_type: "string"
```

## Common Pitfalls

### 1. Forgetting to update 0001_init.up.sql
If you only create an incremental migration, new installations (starting from scratch) will miss the change.

### 2. Modifying internal/db/sqlc/ directly
These files are auto-generated. Your changes will be overwritten on next `sqlc-generate`.

### 3. Nullable vs non-nullable confusion
Check the actual column definition (NULL / NOT NULL) in the schema. The generated Go types differ.

### 4. JSONB query patterns
sqlc doesn't understand JSONB operators natively. Use `sqlc.arg()` for JSONB parameters and handle marshaling in Go.

### 5. Migration ordering
Always use the next available number. Check existing migrations first:

```bash
ls db/migrations/*.up.sql | tail -5
```
