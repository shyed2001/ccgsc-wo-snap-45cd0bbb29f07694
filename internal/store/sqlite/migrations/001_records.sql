-- Path: internal/store/sqlite/migrations/001_records.sql
-- Author: Codex for Claude Opus 5.5 | 2026-11-17 | WWPP-R18.2, R40.2
-- Debug JSON records preserve all module data in atomic transactions, no second truth store.
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS records (
    bucket TEXT NOT NULL,
    id TEXT NOT NULL,
    body TEXT NOT NULL CHECK(json_valid(body)),
    PRIMARY KEY(bucket,id)
);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(1);
