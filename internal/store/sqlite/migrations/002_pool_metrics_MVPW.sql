-- Path: internal/store/sqlite/migrations/002_pool_metrics_MVPW.sql
-- What/why: Slice B pool_metrics sample table for SQLite (WWPP-R37.2, R40.1).
CREATE TABLE IF NOT EXISTS pool_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,
    scope TEXT NOT NULL,
    job_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    value REAL NOT NULL,
    unit TEXT NOT NULL,
    source TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pool_metrics_ts_ix ON pool_metrics (ts);
CREATE INDEX IF NOT EXISTS pool_metrics_scope_job_name_ix ON pool_metrics (scope, job_id, name, ts);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(2);
