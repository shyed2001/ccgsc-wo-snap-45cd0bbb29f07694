-- Path: db/pool_metrics_MVPW.sql
-- What/why: Slice B PostgreSQL pool_metrics sample table (WWPP-R37.2, R40.1).
BEGIN;

CREATE TABLE IF NOT EXISTS pool_metrics (
    id     bigserial        PRIMARY KEY,
    ts     timestamptz      NOT NULL DEFAULT now(),
    scope  text             NOT NULL,
    job_id text             NOT NULL DEFAULT '',
    name   text             NOT NULL,
    value  double precision NOT NULL,
    unit   text             NOT NULL,
    source text             NOT NULL,
    CONSTRAINT pool_metrics_scope_ck  CHECK (btrim(scope) <> ''),
    CONSTRAINT pool_metrics_name_ck   CHECK (btrim(name) <> ''),
    CONSTRAINT pool_metrics_unit_ck   CHECK (btrim(unit) <> ''),
    CONSTRAINT pool_metrics_source_ck CHECK (btrim(source) <> '')
);

CREATE INDEX IF NOT EXISTS pool_metrics_ts_ix             ON pool_metrics (ts);
CREATE INDEX IF NOT EXISTS pool_metrics_scope_job_name_ix ON pool_metrics (scope, job_id, name, ts);

COMMIT;
