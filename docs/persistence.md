# Persistence Plan

Phase 1 introduces durable storage so async jobs, history, deployments, API tokens, and UI
preferences survive pod restarts and can be queried efficiently.

## Storage Technology

- **Engine:** SQLite 3.
- **Location:** `/app/data/model-manager.db` stored on a ReadWriteOnce PVC named
  `model-manager-db`. The Helm chart will mount this PVC at `/app/data`.
- **Rationale:** SQLite keeps the operational burden low (single pod writer, no network hop)
  while satisfying durability + queryability requirements. If concurrency needs grow we can
  swap the driver to Postgres without changing higher layers thanks to the repository pattern
  described below.

## Data Access Layer

```
internal/persistence/
  datastore.go      // interface for Jobs, History, Deployments, Tokens, Preferences
  sqlite/
    datastore.go    // SQLite implementation using modernc.org/sqlite
  memory/           // (optional) in-memory impl for tests
```

- The existing `internal/store` (BoltDB) will remain available behind the same interface during
  the migration window. Once SQLite is stable we will remove Bolt usage.
- All calls from handlers move through the datastore interface (e.g., `datastore.Jobs()`,
  `datastore.History()`), making it easy to inject mocks in tests.

## Schema Overview

```sql
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL,
    stage TEXT,
    progress INTEGER DEFAULT 0,
    message TEXT,
    payload JSON,
    result JSON,
    error TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX jobs_type_idx ON jobs(type);
CREATE INDEX jobs_status_idx ON jobs(status);

CREATE TABLE history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event TEXT NOT NULL,
    model_id TEXT,
    metadata JSON,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE deployments (
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL,
    spec JSON,
    manifest TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    hashed_secret TEXT NOT NULL,
    scopes TEXT NOT NULL,           -- comma separated for now
    last_used_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE preferences (
    id TEXT PRIMARY KEY,
    data JSON NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

The schema leaves room for future tables (notifications, audit logs, GPU inventory snapshots).
Migrations will live in `internal/persistence/migrations` and run automatically on startup.

## Operational Considerations

- SQLite WAL mode for better concurrency.
- Periodic VACUUM + integrity checks triggered via background job (configurable interval).
- Metrics: count of jobs per status, DB size gauge, migration duration histogram.
- Backups: PVC snapshot via Velero or nightly `sqlite3 .backup` job (documented separately).

This plan ensures the backend is ready for the async/job heavy features required by the UI.
