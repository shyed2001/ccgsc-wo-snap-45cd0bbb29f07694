-- T91 / R42.8 | Codex | 2026-11-20: Redis controls in both initial page schemas.
-- =====================================================================
-- pool_config.sql - CCGSC pool admin config storage (PostgreSQL 16)
-- =====================================================================
-- Work order : WO-HC-SCH-01 (hc, vostro, cmdc deepseek-v4-pro) - tmp only
-- WO author  : Claude Opus 5.5 head, 2026-11-17
-- Written by  : hermes MiniMax-M3, 2026-11-17
-- Truth input: F:/tmp/hcwork/ctx_rows_R37_R40.md
--   WWPP-R38.6  versioned config + audit: every Save is a new version
--               (who / when / what / reason), new jobs use it at once,
--               running jobs keep their version, one-click rollback,
--               security log always on
--   WWPP-R38.7  keep the last 10 versions per admin page (the count is
--               itself an admin setting) + any version a running job uses
--               + admin-named pins; older versions are pruned; the
--               who/when/what audit row stays in the security log
--   WWPP-R40.2  PG table pool_config_version (id PK, page, version, json,
--               json_schema_ver, author FK, reason, created_at,
--               pinned_name, in_use) + JSON Schema validates every Save
--   R-97        every table has a primary key; every *_id column is a
--               foreign key
-- Design text : F:/tmp/hcwork/CCGSC_TWO_POOLS_DESIGN.md sections 14, 17, 18
-- Companion   : pool_config.schema.json - JSON Schema 2020-12 for the
--               thread-pool-admin page config; the same document is
--               embedded below as the registry copy so the database can
--               validate a Save without a round trip to the app
-- Status      : tmp only, DRAFT. NOT applied to any database: this work
--               order forbids a DB connection. Review, then apply on a
--               staging PostgreSQL 16.
-- Guardrails  : only pool_config.sql and pool_config.schema.json were
--               written; no DB connection; no network.
--
-- T6.2 fix (hermes MiniMax-M3, 2026-11-17, WO T6.2 PG_STORE): two DDL
-- bugs were found by running the script on PostgreSQL 17.11 and were
-- fixed here.
--   1. pool_config_sync_in_use() recomputed the in_use flag for one
--      version only, so moving a job from version A to version B left
--      version A flagged in_use=true for ever. It now recomputes the
--      flag for the version the row pointed at before and the one it
--      points at now.
--   2. pool_config_guard_version() demanded that the row carried the
--      schema version that is registered right now, on UPDATE as well
--      as on INSERT. As soon as the admin registered a newer schema
--      version, every UPDATE of an older row was rejected - including
--      the UPDATE the job_config_ref trigger needs to clear the in_use
--      flag. The job could then never release its version and the
--      version could never be pruned. The equality check now applies
--      to INSERT; on UPDATE a row that still carries the registered
--      schema version is validated again, and a row written under an
--      older schema version keeps the version it was validated with.
--   Both fixes are reported in F:\tmp\par\t6_2\REPORT.md.
--
-- T6.2 fix (hermes MiniMax-M3, 2026-11-17, WO T6.2 PG_FIX): the
-- PostgreSQL 18 failure of the 8 integration tests, plus two more
-- casts of the same kind, were fixed here.
--   3. The schema validator cast the value of a property to numeric so
--      it could compare it with the minimum and the maximum of a oneOf
--      branch. A property may hold the word "auto" instead of a number
--      (for example tp.wrk.per_tab and tp.wrk.owner), so the cast
--      raised "invalid input syntax for type numeric: \"auto\""
--      (SQLSTATE 22P02) and every Save of such a config failed.
--      PostgreSQL 17 short-circuits the type check at run time, so the
--      bug is invisible there; PostgreSQL 18 no longer evaluates the
--      type check lazily and the planner folds the cast out of the
--      guard while it plans the SQL function (PostgreSQL bug #19428:
--      "lazy evaluation of type checking in CASE in SQL functions
--      including subqueries no longer works in 18"). The validator now
--      compares with the jsonb operators, which compare numbers
--      numerically and never raise, and it never casts a config value
--      to a numeric type.
--   4. The cross-field rule of pool_config_guard_version() cast
--      tp.wrk.min and tp.wrk.max to integer for the same reason and is
--      fixed the same way.
--   All three fixes are reported in F:\tmp\par\t6_2\REPORT.md.
--
-- Page ids (the 7 admin pages, WWPP-R38.5 / R38.5b / R39.1 spelling):
--   monitor, pool-monitor, api-monitor, thread-pool-admin,
--   process-pool-admin, thread-api-admin, process-api-admin
--
-- "in_use" (WWPP-R40.2) is a real column on pool_config_version. It is kept
-- in step with job_config_ref by a trigger: a job keeps the version it
-- started with (WWPP-R38.6), and a version that is in use is never pruned
-- (WWPP-R38.7). The pool_config_version_state view exposes the same flags
-- together with pinned and is_current.
--
-- WO FIX-E fix (hermes MiniMax-M3, 2026-11-18, WO FIX-E PG_STORE), against
-- the review WEBCHAT_R1_qwen_pgstore_20261117.md (38 rows). What changed:
--   1. BLOCKER: a registered JSON Schema is seeded for ALL 7 admin pages.
--      Before, only thread-pool-admin had one, so the other 6 pages could
--      never be saved (Go schemaFor -> ErrInvalidConfig).
--   2. BLOCKER: pool_config_rollback() revalidates the old config against
--      the schema version that is registered NOW and stamps that version.
--      Before, it copied the old json_schema_ver, and the guard demands the
--      registered one, so every rollback raised 22023 after a schema bump.
--   3. The in_use flag can no longer be stuck: the view derives it from
--      job_config_ref, and the trigger locks the version rows before it
--      recomputes the flag (the READ COMMITTED write skew).
--   4. One advisory lock per page is shared by save, rollback, prune, pin
--      and attach, so a prune cannot delete a version an attach is using.
--   5. The audit actor is NEVER taken from the client-settable GUC
--      app.actor_id any more: it comes from NEW.author_id, from the row
--      that carries it (pinned_by) or from the author argument of the
--      function that performs the change. A spoofed GUC can no longer lie
--      in the security log, and a non-numeric GUC can no longer kill every
--      save with 22P02.
--   6. The security log is append-only for TRUNCATE as well: a BEFORE
--      TRUNCATE statement trigger refuses it and TRUNCATE is revoked.
--   7. A config version is immutable: the page, the version number, the
--      config, the schema version, the author, the reason and the creation
--      time of a version never change. Only the derived in_use flag and the
--      admin pin may change, and both are audited.
--   8. pool_config_json_schema keeps the history of the schema documents:
--      the primary key is (page, schema_version) and one row per page is
--      the current one.
--   9. oneOf means EXACTLY ONE branch, every keyword of a branch is
--      evaluated, and a branch with no keyword matches nothing (fail
--      closed). The database and the app now agree.
--  10. The job id check and the Go check are the same: three segments of
--      [A-Za-z0-9_-], the dots escaped, the length capped at 128.
--  11. The retention counts the newest FREE versions: a pinned version and
--      a version a running job uses are kept IN ADDITION (WWPP-R38.7).
--  12. The state view uses a window function and runs as the invoker.
-- =====================================================================

-- The script installs into the public schema, so every name below is
-- unambiguous and the SECURITY DEFINER functions cannot be pointed at a
-- schema of an attacker by a search_path change.
SET search_path = public, pg_temp;

BEGIN;

-- ---------------------------------------------------------------------
-- 0. Extensions and helpers
-- ---------------------------------------------------------------------

-- touch updated_at on every UPDATE of a table that carries it
CREATE OR REPLACE FUNCTION pool_config_touch_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$fn$;

-- ---------------------------------------------------------------------
-- 1. admin_user - account store A (admin + director), WWPP-R38.3
--    One person may hold several roles in one store, so roles is a flag
--    array; every *_id elsewhere points here.
-- ---------------------------------------------------------------------
CREATE TABLE admin_user (
    id              bigserial   PRIMARY KEY,
    store           text        NOT NULL DEFAULT 'A',
    username        text        NOT NULL,
    display_name    text,
    roles           text[]      NOT NULL DEFAULT ARRAY['admin']::text[],
    password_hash   text,
    sso_subject     text,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_user_store_ck      CHECK (store = 'A'),
    CONSTRAINT admin_user_username_ck   CHECK (username ~ '^[a-z0-9._-]{3,64}$'),
    CONSTRAINT admin_user_roles_ck       CHECK (roles <@ ARRAY['admin','director']::text[] AND cardinality(roles) >= 1),
    CONSTRAINT admin_user_credential_ck CHECK (password_hash IS NOT NULL OR sso_subject IS NOT NULL)
);

CREATE UNIQUE INDEX admin_user_username_lower_uq ON admin_user (lower(username));
CREATE UNIQUE INDEX admin_user_sso_subject_uq    ON admin_user (sso_subject) WHERE sso_subject IS NOT NULL;

CREATE TRIGGER admin_user_touch_trg BEFORE UPDATE ON admin_user
    FOR EACH ROW EXECUTE FUNCTION pool_config_touch_updated_at();

COMMENT ON TABLE  admin_user IS 'Account store A: admin and director roles (WWPP-R38.3). One person may hold several roles in the store; admin_user stores the role flags.';
COMMENT ON COLUMN admin_user.id           IS 'Primary key; referenced by pool_config_version.author_id and security_audit_log.actor_id.';
COMMENT ON COLUMN admin_user.store        IS 'Account store letter; this table is store A only (admin + director).';
COMMENT ON COLUMN admin_user.username     IS 'Login name, lower-case, unique per store.';
COMMENT ON COLUMN admin_user.display_name IS 'Name shown in the admin UI and in audit rows.';
COMMENT ON COLUMN admin_user.roles        IS 'Role flags inside store A: admin and/or director (WWPP-R38.3).';
COMMENT ON COLUMN admin_user.password_hash IS 'Hashed password; null when the account uses single sign-on.';
COMMENT ON COLUMN admin_user.sso_subject  IS 'Single-sign-on subject; null when the account uses a password.';
COMMENT ON COLUMN admin_user.is_active    IS 'Inactive accounts cannot author a config version.';
COMMENT ON COLUMN admin_user.created_at   IS 'Creation time.';
COMMENT ON COLUMN admin_user.updated_at   IS 'Last change time; kept current by trigger.';

-- ---------------------------------------------------------------------
-- 2. pool_admin_page - the 7 admin page ids (WWPP-R38.5, R38.5b)
--    Gives the page column a real parent row so both pool_config_version
--    and pool_config_retention can carry a foreign key (R-97).
-- ---------------------------------------------------------------------
CREATE TABLE pool_admin_page (
    id          text        PRIMARY KEY,
    tier        smallint    NOT NULL,
    title       text        NOT NULL,
    html_file   text        NOT NULL,
    manual_path text        NOT NULL,
    sort_order  smallint    NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pool_admin_page_id_ck   CHECK (id IN ('monitor','pool-monitor','api-monitor','thread-pool-admin','process-pool-admin','thread-api-admin','process-api-admin')),
    CONSTRAINT pool_admin_page_tier_ck CHECK (tier IN (1,2,3))
);

COMMENT ON TABLE  pool_admin_page IS 'The 7 admin and monitor pages (WWPP-R38.5, R38.5b). Tier 1 = brief combined monitor, tier 2 = medium monitors, tier 3 = full detail admin pages.';
COMMENT ON COLUMN pool_admin_page.id          IS 'Page id used by pool_config_version.page and pool_config_retention.page.';
COMMENT ON COLUMN pool_admin_page.tier        IS 'Monitor tier: 1 brief, 2 medium, 3 full detail.';
COMMENT ON COLUMN pool_admin_page.title       IS 'Page title shown in the nav.';
COMMENT ON COLUMN pool_admin_page.html_file   IS 'HTML file name of the page under /pools/.';
COMMENT ON COLUMN pool_admin_page.manual_path IS 'Full manual page path built from the one markdown source.';
COMMENT ON COLUMN pool_admin_page.sort_order  IS 'Order in the shared nav bar (WWPP-R38.2).';
COMMENT ON COLUMN pool_admin_page.created_at  IS 'Creation time.';

-- ---------------------------------------------------------------------
-- 3. pool_config_json_schema - registered JSON Schemas per page
--    Every Save is validated against the registered schema (WWPP-R40.2).
-- ---------------------------------------------------------------------
-- WO FIX-E: the table keeps the HISTORY of the schema documents of a page.
-- The primary key is (page, schema_version) and is_current marks the one
-- document the next Save is validated against. Before, the primary key was
-- (page) alone, so a schema bump overwrote the old document and the old
-- json_schema_ver became unresolvable.
CREATE TABLE pool_config_json_schema (
    page           text        NOT NULL REFERENCES pool_admin_page(id) ON DELETE RESTRICT,
    schema_name    text        NOT NULL,
    schema_version text        NOT NULL,
    schema         jsonb       NOT NULL,
    is_current     boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pool_config_json_schema_pk        PRIMARY KEY (page, schema_version),
    CONSTRAINT pool_config_json_schema_schema_ck CHECK (jsonb_typeof(schema) = 'object'),
    CONSTRAINT pool_config_json_schema_version_ck CHECK (btrim(schema_version) <> '')
);

-- at most one current schema document per page
CREATE UNIQUE INDEX pool_config_json_schema_current_uq
    ON pool_config_json_schema (page) WHERE is_current;

CREATE TRIGGER pool_config_json_schema_touch_trg BEFORE UPDATE ON pool_config_json_schema
    FOR EACH ROW EXECUTE FUNCTION pool_config_touch_updated_at();

-- registering a new document makes the previous one history
CREATE OR REPLACE FUNCTION pool_config_json_schema_current()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    IF NEW.is_current THEN
        UPDATE pool_config_json_schema s
           SET is_current = false, updated_at = now()
         WHERE s.page = NEW.page
           AND s.schema_version <> NEW.schema_version
           AND s.is_current;
    END IF;
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER pool_config_json_schema_current_trg BEFORE INSERT OR UPDATE ON pool_config_json_schema
    FOR EACH ROW EXECUTE FUNCTION pool_config_json_schema_current();

COMMENT ON TABLE  pool_config_json_schema IS 'Registered JSON Schemas per admin page, with their history: the primary key is (page, schema_version) and exactly one document per page is the current one (is_current). A Save must carry a json_schema_ver equal to the current schema_version, and its config must pass the current schema (WWPP-R40.2).';
COMMENT ON COLUMN pool_config_json_schema.page           IS 'Page id; foreign key to pool_admin_page (R-97).';
COMMENT ON COLUMN pool_config_json_schema.schema_name    IS 'Logical name of the schema document, for example thread-pool-admin. Named schema_name, not schema_id, so that every *_id column in this schema is a real foreign key (R-97).';
COMMENT ON COLUMN pool_config_json_schema.schema_version IS 'Version of the schema document; copied into pool_config_version.json_schema_ver on Save. Part of the primary key, so the history of the documents is kept.';
COMMENT ON COLUMN pool_config_json_schema.is_current   IS 'True for the one document of the page that the next Save is validated against; the other documents are history.';
COMMENT ON COLUMN pool_config_json_schema.schema         IS 'The JSON Schema 2020-12 document itself.';
COMMENT ON COLUMN pool_config_json_schema.created_at     IS 'Creation time.';
COMMENT ON COLUMN pool_config_json_schema.updated_at     IS 'Last change time; kept current by trigger.';

-- ---------------------------------------------------------------------
-- 4. pool_config_version - the versioned page config (WWPP-R40.2)
-- ---------------------------------------------------------------------
CREATE TABLE pool_config_version (
    id              bigserial   PRIMARY KEY,
    page            text        NOT NULL REFERENCES pool_admin_page(id) ON DELETE RESTRICT,
    version         integer     NOT NULL DEFAULT 0,
    config          jsonb       NOT NULL,
    json_schema_ver text        NOT NULL,
    author_id       bigint      NOT NULL REFERENCES admin_user(id) ON DELETE RESTRICT,
    reason          text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    pinned_name     text,
    pinned_by       bigint      REFERENCES admin_user(id) ON DELETE RESTRICT,
    in_use          boolean     NOT NULL DEFAULT false,
    CONSTRAINT pool_config_version_page_ck     CHECK (page IN ('monitor','pool-monitor','api-monitor','thread-pool-admin','process-pool-admin','thread-api-admin','process-api-admin')),
    CONSTRAINT pool_config_version_version_ck  CHECK (version > 0),
    CONSTRAINT pool_config_version_config_ck   CHECK (jsonb_typeof(config) = 'object'),
    CONSTRAINT pool_config_version_reason_ck   CHECK (btrim(reason) <> ''),
    CONSTRAINT pool_config_version_pinned_name_ck CHECK (pinned_name IS NULL OR char_length(btrim(pinned_name)) BETWEEN 1 AND 64),
    CONSTRAINT pool_config_version_uniq       UNIQUE (page, version),
    CONSTRAINT pool_config_version_pinned_uniq UNIQUE (page, pinned_name)
);

COMMENT ON TABLE  pool_config_version IS 'One row per Save of one admin page: the versioned config with who, when, what and why (WWPP-R38.6, WWPP-R40.2). New jobs use the newest version at once, running jobs keep their own version (see job_config_ref).';
COMMENT ON COLUMN pool_config_version.id              IS 'Primary key; referenced by job_config_ref.config_version_id.';
COMMENT ON COLUMN pool_config_version.page            IS 'Admin page id; foreign key to pool_admin_page and restricted to the 7 known ids (R-97).';
COMMENT ON COLUMN pool_config_version.version         IS 'Version number inside the page, assigned by trigger, starting at 1; unique per page.';
COMMENT ON COLUMN pool_config_version.config          IS 'The page settings as JSON, validated against the registered JSON Schema on every Save.';
COMMENT ON COLUMN pool_config_version.json_schema_ver IS 'Version of the JSON Schema used to validate this config; must equal the registered schema_version.';
COMMENT ON COLUMN pool_config_version.author_id       IS 'Admin author of this version; foreign key to admin_user (R-97).';
COMMENT ON COLUMN pool_config_version.reason          IS 'Required reason for the change; written into the security log.';
COMMENT ON COLUMN pool_config_version.created_at      IS 'Creation time of this version.';
COMMENT ON COLUMN pool_config_version.pinned_name     IS 'Optional admin-named pin, for example a thesis run; unique per page, 1 to 64 characters, and never pruned (WWPP-R38.7).';
COMMENT ON COLUMN pool_config_version.pinned_by       IS 'The admin who holds the pin; foreign key to admin_user (R-97). It is the actor of the pin audit row, so the audit trail never depends on a client-settable session variable.';
COMMENT ON COLUMN pool_config_version.in_use          IS 'True while a running job keeps this version; kept in sync with job_config_ref by trigger, so pruning never removes a version in use (WWPP-R38.6, WWPP-R38.7, WWPP-R40.2).';

-- ---------------------------------------------------------------------
-- 5. pool_config_retention - how many versions to keep per page
--    (WWPP-R38.7: the count is itself an admin setting, default 10)
-- ---------------------------------------------------------------------
CREATE TABLE pool_config_retention (
    page       text        PRIMARY KEY REFERENCES pool_admin_page(id) ON DELETE RESTRICT,
    keep_last  integer     NOT NULL DEFAULT 10,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pool_config_retention_keep_last_ck CHECK (keep_last >= 1)
);

CREATE TRIGGER pool_config_retention_touch_trg BEFORE UPDATE ON pool_config_retention
    FOR EACH ROW EXECUTE FUNCTION pool_config_touch_updated_at();

COMMENT ON TABLE  pool_config_retention IS 'Number of config versions kept per admin page (WWPP-R38.7). Default 10; the owner may change it. A version still used by a running job and a pinned version are kept in addition.';
COMMENT ON COLUMN pool_config_retention.page       IS 'Admin page id; primary key and foreign key to pool_admin_page (R-97).';
COMMENT ON COLUMN pool_config_retention.keep_last  IS 'How many newest versions of that page survive pruning; minimum 1.';
COMMENT ON COLUMN pool_config_retention.updated_at IS 'Last change time; kept current by trigger.';

-- ---------------------------------------------------------------------
-- 6. job_config_ref - the version a running job keeps (WWPP-R38.6)
--    A job pins its config version for its whole life; pruning must
--    never touch a referenced version.
-- ---------------------------------------------------------------------
CREATE TABLE job_config_ref (
    job_id           text        PRIMARY KEY,
    config_version_id bigint     NOT NULL REFERENCES pool_config_version(id) ON DELETE RESTRICT,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- WO FIX-E: the separator dots are escaped and the segment classes do
    -- NOT contain a dot any more, so only a three-part id passes and a
    -- five-part id cannot slip through. The length is capped at 128.
    CONSTRAINT job_config_ref_job_id_ck CHECK (
        char_length(job_id) <= 128
        AND job_id ~ E'^[A-Za-z0-9][A-Za-z0-9_-]*\\.[A-Za-z0-9][A-Za-z0-9_-]*\\.[A-Za-z0-9][A-Za-z0-9_-]*$')
);


-- keep pool_config_version.in_use in step with job_config_ref
-- T6.2 fix (hermes MiniMax-M3, 2026-11-17): the flag is recomputed for the version the row
-- pointed at BEFORE and the version it points at NOW. The old code recomputed only one of
-- them, so moving a job from version A to version B left version A flagged in_use=true for
-- ever, even though no job used it any more.
-- WO FIX-E (2026-11-18): the rows are LOCKED (FOR UPDATE) before the flag is recomputed.
-- Without the lock two concurrent detaches of the last two jobs of one version both saw
-- the other job in the EXISTS and both left the flag true (the READ COMMITTED write skew
-- of the review). The view pool_config_version_state derives the flag from job_config_ref
-- as well, so the flag that the app reads is always the truth.
CREATE OR REPLACE FUNCTION pool_config_sync_in_use()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_version_ids bigint[];
BEGIN
    v_version_ids := ARRAY[
        COALESCE(NEW.config_version_id, OLD.config_version_id),
        COALESCE(OLD.config_version_id, NEW.config_version_id)
    ];

    -- lock the rows first, so two transactions that change the references of the
    -- same version cannot both read a stale set of references
    PERFORM 1 FROM pool_config_version v WHERE v.id = ANY(v_version_ids) FOR UPDATE;

    UPDATE pool_config_version v
       SET in_use = EXISTS (SELECT 1 FROM job_config_ref j WHERE j.config_version_id = v.id)
     WHERE v.id = ANY(v_version_ids);

    RETURN NULL;
END;
$fn$;

CREATE TRIGGER job_config_ref_in_use_trg
    AFTER INSERT OR UPDATE OR DELETE ON job_config_ref
    FOR EACH ROW EXECUTE FUNCTION pool_config_sync_in_use();

COMMENT ON TABLE  job_config_ref IS 'The config version each job keeps for its whole life. New jobs take the newest version at once, running jobs keep the version they started with (WWPP-R38.6); referenced versions are never pruned (WWPP-R38.7).';
COMMENT ON COLUMN job_config_ref.job_id            IS 'Job id in the shape serverId.poolId.jobId (WWPP-R26.4); primary key. It points at the job row of the pool core, which lives outside this DDL, so no foreign key is declared here; the FK to the core job table is added when that table is defined in the same database (R-97).';
COMMENT ON COLUMN job_config_ref.config_version_id IS 'Version this job is pinned to; foreign key to pool_config_version with delete restricted (R-97).';
COMMENT ON COLUMN job_config_ref.created_at        IS 'Time the job was pinned to that version.';

-- ---------------------------------------------------------------------
-- 7. security_audit_log - append-only security log (WWPP-R25.1)
--    Always on, never off, never deleted; it is the record behind the
--    who / when / what audit row that survives pruning (R38.7).
-- ---------------------------------------------------------------------
CREATE TABLE security_audit_log (
    id           bigserial   PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor_id     bigint      REFERENCES admin_user(id) ON DELETE RESTRICT,
    action       text        NOT NULL,
    page         text        REFERENCES pool_admin_page(id) ON DELETE RESTRICT,
    from_version integer,
    to_version   integer,
    diff         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT security_audit_log_action_ck CHECK (btrim(action) <> ''),
    CONSTRAINT security_audit_log_diff_ck   CHECK (jsonb_typeof(diff) = 'object')
);

COMMENT ON TABLE  security_audit_log IS 'Append-only security log: authentication, keys, admin setting changes, approvals and revocations (WWPP-R25.1). It is always on and its rows are never deleted, so the audit row of a pruned version survives (WWPP-R38.7).';
COMMENT ON COLUMN security_audit_log.id           IS 'Primary key of the append-only log.';
COMMENT ON COLUMN security_audit_log.at           IS 'Event time.';
COMMENT ON COLUMN security_audit_log.actor_id     IS 'Actor; foreign key to admin_user, null for a system action (R-97).';
COMMENT ON COLUMN security_audit_log.action       IS 'Action name, for example config_version_saved, config_version_pruned, config_version_rolled_back, config_version_pinned, retention_changed.';
COMMENT ON COLUMN security_audit_log.page         IS 'Admin page id; foreign key to pool_admin_page, null for a page-independent action (R-97).';
COMMENT ON COLUMN security_audit_log.from_version IS 'Version before the change.';
COMMENT ON COLUMN security_audit_log.to_version   IS 'Version after the change.';
COMMENT ON COLUMN security_audit_log.diff         IS 'What changed between the two versions as JSON.';

-- ---------------------------------------------------------------------
-- 7a. Indexes on the tables created above
--     They live here, after every table exists, so the script loads top
--     to bottom on an empty database.
-- ---------------------------------------------------------------------
-- WO FIX-E: the two indexes that duplicated the UNIQUE constraints
-- (page, version) and (page, pinned_name) are dropped; the constraints
-- already carry the index.
CREATE INDEX pool_config_version_author_ix        ON pool_config_version (author_id);
CREATE INDEX job_config_ref_version_ix           ON job_config_ref (config_version_id);
CREATE INDEX security_audit_log_page_at_ix       ON security_audit_log (page, at DESC);
CREATE INDEX security_audit_log_actor_at_ix      ON security_audit_log (actor_id, at DESC);

-- ---------------------------------------------------------------------
-- 8. Version numbering, schema validation and the audit trail
-- ---------------------------------------------------------------------

-- the advisory lock of one page. Save, rollback, prune, pin, attach and
-- detach all take it, so they never work on the same page at the same time.
-- The lock is transaction scoped: it is released when the transaction ends,
-- whatever the outcome is.
CREATE OR REPLACE FUNCTION pool_config_lock_page(p_page text)
RETURNS void
LANGUAGE sql
AS $fn$
    SELECT pg_advisory_xact_lock(hashtext('pool_config_version:' || p_page));
$fn$;

-- assign the next version number inside the page, serialized per page
-- WO FIX-E: the lock of the page is taken first, so the max() below is read
-- under the lock. Under READ COMMITTED (the level the app pins) the read is
-- a fresh snapshot and the number is dense; under a stronger level the
-- unique constraint can still fire, and the app retries 23505 and 40001.
CREATE OR REPLACE FUNCTION pool_config_assign_version()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    PERFORM pool_config_lock_page(NEW.page);
    -- the version number is always the next one inside the page; a value
    -- supplied by the caller is ignored so the sequence stays dense
    NEW.version := COALESCE(
        (SELECT max(v.version) FROM pool_config_version v WHERE v.page = NEW.page), 0) + 1;
    RETURN NEW;
END;
$fn$;

-- does a JSON value match a JSON Schema type name?
-- jsonb_typeof() never returns 'integer', so an integer is a number whose
-- text form has no fraction (JSON Schema 2020-12 semantics).
-- WO FIX-E: the type may be a LIST of type names ("string" or "null"), and
-- the dot of the number pattern is escaped (it was a wildcard before).
CREATE OR REPLACE FUNCTION pool_config_json_type_ok(p_value jsonb, p_type jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $fn$
    SELECT CASE jsonb_typeof(p_type)
        WHEN 'array' THEN COALESCE(
            (SELECT bool_or(pool_config_json_type_ok(p_value, t))
               FROM jsonb_array_elements(p_type) AS e(t)),
            false)
        WHEN 'string' THEN CASE p_type #>> '{}'
            WHEN 'integer' THEN jsonb_typeof(p_value) = 'number'
                                  AND (p_value #>> '{}') ~ E'^-?[0-9]+(\\.0+)?$'
            WHEN 'number'  THEN jsonb_typeof(p_value) = 'number'
            ELSE jsonb_typeof(p_value) = p_type #>> '{}'
        END
        ELSE false
    END;
$fn$;

-- does a JSON value lie inside the numeric bounds of a JSON Schema branch?
-- T6.2 fix (hermes MiniMax-M3, 2026-11-17, WO T6.2 PG_FIX): a schema value may be the
-- word "auto" instead of a number, so nothing here may cast the value to numeric: the
-- cast raises "invalid input syntax for type numeric: \"auto\"". PostgreSQL 18 hoists
-- such a cast out of the type guard while it plans the SQL function, so the old code
-- failed on every Save that carried "auto" (8 integration tests on PostgreSQL 18).
-- The jsonb comparison operators compare numbers numerically and never raise, so they
-- are used instead of a numeric cast.
CREATE OR REPLACE FUNCTION pool_config_json_in_bounds(p_value jsonb, p_branch jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $fn$
    SELECT CASE
        WHEN jsonb_typeof(p_value) <> 'number' THEN false
        WHEN p_branch ? 'minimum'
             AND jsonb_typeof(p_branch -> 'minimum') = 'number'
             AND p_value < (p_branch -> 'minimum')::jsonb THEN false
        WHEN p_branch ? 'maximum'
             AND jsonb_typeof(p_branch -> 'maximum') = 'number'
             AND p_value > (p_branch -> 'maximum')::jsonb THEN false
        ELSE true
    END;
$fn$;

-- does a JSON value match ONE branch of a JSON Schema oneOf?
-- WO FIX-E: every keyword of the branch is evaluated and a branch without a
-- single keyword matches nothing (fail closed). Before, a branch with
-- neither const nor type fell into an ELSE that returned true, so an
-- enum-only or a bounds-only branch accepted any value.
CREATE OR REPLACE FUNCTION pool_config_branch_ok(p_value jsonb, p_branch jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $fn$
    SELECT (
        p_branch IS NOT NULL
        AND jsonb_typeof(p_branch) = 'object'
        AND (p_branch ? 'const' OR p_branch ? 'type' OR p_branch ? 'enum'
             OR p_branch ? 'minimum' OR p_branch ? 'maximum'
             OR p_branch ? 'required' OR p_branch ? 'properties'
             OR p_branch ? 'pattern')
        AND (NOT (p_branch ? 'const')
             OR p_value = p_branch -> 'const')
        AND (NOT (p_branch ? 'type')
             OR pool_config_json_type_ok(p_value, p_branch -> 'type'))
        AND (NOT (p_branch ? 'enum')
             OR (p_branch -> 'enum') @> jsonb_build_array(p_value))
        AND (NOT (p_branch ? 'minimum' OR p_branch ? 'maximum')
             OR pool_config_json_in_bounds(p_value, p_branch))
        AND (NOT (p_branch ? 'pattern')
             OR (jsonb_typeof(p_value) = 'string'
                 AND (p_value #>> '{}') ~ (p_branch ->> 'pattern')))
        AND (NOT (p_branch ? 'required') OR (
                jsonb_typeof(p_value) = 'object'
                AND NOT EXISTS (
                    SELECT 1 FROM jsonb_array_elements_text(p_branch -> 'required') AS r(k)
                     WHERE NOT (p_value ? r.k))))
        AND (NOT (p_branch ? 'properties') OR (
                jsonb_typeof(p_value) = 'object'
                AND NOT EXISTS (
                    SELECT 1 FROM jsonb_each(p_branch -> 'properties') AS pr(key, spec)
                     WHERE p_value ? pr.key
                       AND NOT pool_config_branch_ok(p_value -> pr.key, pr.spec))))
    );
$fn$;

-- does a JSON value match exactly one branch of a JSON Schema oneOf?
-- WO FIX-E: oneOf means EXACTLY ONE branch, the same rule the app uses. The
-- old code used bool_or, so "matches at least one branch" was enough and the
-- database and the app disagreed.
CREATE OR REPLACE FUNCTION pool_config_one_of_ok(p_value jsonb, p_branches jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $fn$
    SELECT CASE jsonb_typeof(p_branches)
        WHEN 'array' THEN (
            SELECT count(*) FILTER (WHERE pool_config_branch_ok(p_value, branch)) = 1
              FROM jsonb_array_elements(p_branches) AS b(branch))
        ELSE false
    END;
$fn$;

-- validate a config against the registered JSON Schema of its page.
-- The database check is the structural subset that PostgreSQL can
-- express: required properties, JSON types, enums, constants, numeric
-- bounds, string lengths, the property names and additionalProperties=false.
-- The authoritative full JSON Schema 2020-12 validation runs in the app on
-- every Save (WWPP-R40.2).
CREATE OR REPLACE FUNCTION pool_config_validate_schema(p_page text, p_config jsonb)
RETURNS void
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_schema   jsonb;
    v_props    jsonb;
    v_req      jsonb;
    v_key      text;
    v_spec     jsonb;
    v_type     jsonb;
    v_enum     jsonb;
BEGIN
    IF p_config IS NULL OR jsonb_typeof(p_config) <> 'object' THEN
        RAISE EXCEPTION 'config for page % must be a JSON object', p_page USING ERRCODE = '22023';
    END IF;

    -- WO FIX-E: the document that is registered NOW is the authority.
    SELECT s.schema INTO v_schema FROM pool_config_json_schema s
     WHERE s.page = p_page AND s.is_current;
    IF v_schema IS NULL THEN
        RAISE EXCEPTION 'no JSON Schema is registered for page %', p_page USING ERRCODE = '22023';
    END IF;

    v_props := COALESCE(v_schema -> 'properties', '{}'::jsonb);
    v_req   := COALESCE(v_schema -> 'required', '[]'::jsonb);

    -- the type of the document itself
    IF v_schema ? 'type' AND NOT pool_config_json_type_ok(p_config, v_schema -> 'type') THEN
        RAISE EXCEPTION 'config for page % must be %', p_page, v_schema ->> 'type' USING ERRCODE = '22023';
    END IF;

    FOR v_key IN SELECT jsonb_array_elements_text(v_req) LOOP
        IF NOT (p_config ? v_key) THEN
            RAISE EXCEPTION 'config for page % is missing the required property %', p_page, v_key USING ERRCODE = '22023';
        END IF;
    END LOOP;

    FOR v_key, v_spec IN SELECT key, value FROM jsonb_each(v_props) LOOP
        IF NOT (p_config ? v_key) THEN
            CONTINUE;
        END IF;

        v_type := v_spec -> 'type';
        IF v_type IS NOT NULL AND NOT pool_config_json_type_ok(p_config -> v_key, v_type) THEN
            RAISE EXCEPTION 'property % on page % must be % but is %', v_key, p_page, v_type,
                jsonb_typeof(p_config -> v_key) USING ERRCODE = '22023';
        END IF;

        v_enum := v_spec -> 'enum';
        IF v_enum IS NOT NULL AND NOT (v_enum @> jsonb_build_array(p_config -> v_key)) THEN
            RAISE EXCEPTION 'property % on page % has a value outside its allowed list', v_key, p_page USING ERRCODE = '22023';
        END IF;

        IF v_spec ? 'const' AND (p_config -> v_key) <> (v_spec -> 'const') THEN
            RAISE EXCEPTION 'property % on page % must be %', v_key, p_page, v_spec -> 'const' USING ERRCODE = '22023';
        END IF;

        -- oneOf means exactly one branch
        IF v_spec ? 'oneOf' AND NOT pool_config_one_of_ok(p_config -> v_key, v_spec -> 'oneOf') THEN
            RAISE EXCEPTION 'property % on page % matches none of its allowed forms', v_key, p_page USING ERRCODE = '22023';
        END IF;

        -- the numeric bounds; a value that is not a number is not bounded
        IF jsonb_typeof(p_config -> v_key) = 'number' THEN
            IF v_spec ? 'minimum'
               AND jsonb_typeof(v_spec -> 'minimum') = 'number'
               AND (p_config -> v_key) < (v_spec -> 'minimum') THEN
                RAISE EXCEPTION 'property % on page % is below its minimum %', v_key, p_page,
                    v_spec ->> 'minimum' USING ERRCODE = '22023';
            END IF;
            IF v_spec ? 'maximum'
               AND jsonb_typeof(v_spec -> 'maximum') = 'number'
               AND (p_config -> v_key) > (v_spec -> 'maximum') THEN
                RAISE EXCEPTION 'property % on page % is above its maximum %', v_key, p_page,
                    v_spec ->> 'maximum' USING ERRCODE = '22023';
            END IF;
        END IF;

        -- the length of a string, counted in characters and not in bytes
        IF jsonb_typeof(p_config -> v_key) = 'string' THEN
            IF v_spec ? 'minLength'
               AND jsonb_typeof(v_spec -> 'minLength') = 'number'
               AND (v_spec ->> 'minLength') ~ '^[0-9]+$'
               AND char_length(p_config ->> v_key) < (v_spec ->> 'minLength')::int THEN
                RAISE EXCEPTION 'property % on page % is shorter than its minimum length %', v_key, p_page,
                    v_spec ->> 'minLength' USING ERRCODE = '22023';
            END IF;
            IF v_spec ? 'maxLength'
               AND jsonb_typeof(v_spec -> 'maxLength') = 'number'
               AND (v_spec ->> 'maxLength') ~ '^[0-9]+$'
               AND char_length(p_config ->> v_key) > (v_spec ->> 'maxLength')::int THEN
                RAISE EXCEPTION 'property % on page % is longer than its maximum length %', v_key, p_page,
                    v_spec ->> 'maxLength' USING ERRCODE = '22023';
            END IF;
        END IF;
    END LOOP;

    -- the names of the properties
    IF v_schema ? 'propertyNames' THEN
        FOR v_key IN SELECT jsonb_object_keys(p_config) LOOP
            IF NOT pool_config_branch_ok(to_jsonb(v_key), v_schema -> 'propertyNames') THEN
                RAISE EXCEPTION 'property name % is not allowed on page %', v_key, p_page USING ERRCODE = '22023';
            END IF;
        END LOOP;
    END IF;

    IF (v_schema -> 'additionalProperties') = 'false'::jsonb THEN
        FOR v_key IN SELECT jsonb_object_keys(p_config) LOOP
            IF NOT (v_props ? v_key) THEN
                RAISE EXCEPTION 'property % is not part of the schema of page %', v_key, p_page USING ERRCODE = '22023';
            END IF;
        END LOOP;
    END IF;
END;
$fn$;

-- gate every INSERT and UPDATE of a config version
-- WO FIX-E (2026-11-18): a config version is IMMUTABLE. The page, the version
-- number, the config, the schema version, the author, the reason and the
-- creation time of a version never change; only the derived in_use flag and
-- the admin pin may change. An UPDATE that touches an immutable column is
-- refused, so the history cannot be rewritten silently.
-- The trigger also returns early for an UPDATE that changes nothing of the
-- config, so the derived in_use flag can always be cleared - the old code
-- revalidated the config on every UPDATE and froze the flag of a version
-- after a schema bump (the BUG-2 redux of the review).
CREATE OR REPLACE FUNCTION pool_config_guard_version()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_schema_ver text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.page IS DISTINCT FROM OLD.page
           OR NEW.version IS DISTINCT FROM OLD.version
           OR NEW.config IS DISTINCT FROM OLD.config
           OR NEW.json_schema_ver IS DISTINCT FROM OLD.json_schema_ver
           OR NEW.author_id IS DISTINCT FROM OLD.author_id
           OR NEW.reason IS DISTINCT FROM OLD.reason
           OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'a config version is immutable: the page, the version, the config, the schema version, the author, the reason and the creation time of version % never change',
                OLD.version USING ERRCODE = '22023';
        END IF;
        -- Nothing of the config changed, so there is nothing to validate
        -- again. The in_use flag and the admin pin may still change.
        RETURN NEW;
    END IF;

    SELECT s.schema_version INTO v_schema_ver FROM pool_config_json_schema s
     WHERE s.page = NEW.page AND s.is_current;

    IF v_schema_ver IS NULL THEN
        -- no schema registered for this page yet: the version is still
        -- allowed, but it must say which schema version it was written for
        IF NEW.json_schema_ver IS NULL OR btrim(NEW.json_schema_ver) = '' THEN
            RAISE EXCEPTION 'page % has no registered JSON Schema, so json_schema_ver must be given explicitly', NEW.page USING ERRCODE = '22023';
        END IF;
        RETURN NEW;
    END IF;

    -- A NEW version must carry the version that is registered right now.
    IF NEW.json_schema_ver IS NULL OR NEW.json_schema_ver <> v_schema_ver THEN
        RAISE EXCEPTION 'json_schema_ver % does not match the registered schema version % of page %',
            NEW.json_schema_ver, v_schema_ver, NEW.page USING ERRCODE = '22023';
    END IF;
    PERFORM pool_config_validate_schema(NEW.page, NEW.config);

    -- cross-field rule that JSON Schema 2020-12 cannot express
    -- T6.2 fix (hermes MiniMax-M3, 2026-11-17): the two numbers are compared with the jsonb
    -- operators, never cast. A cast of the word "auto" raised
    -- "invalid input syntax for type integer: \"auto\"".
    IF NEW.config ? 'tp.wrk.min' AND NEW.config ? 'tp.wrk.max' THEN
        IF jsonb_typeof(NEW.config -> 'tp.wrk.min') = 'number'
           AND jsonb_typeof(NEW.config -> 'tp.wrk.max') = 'number'
           AND (NEW.config -> 'tp.wrk.min') > (NEW.config -> 'tp.wrk.max') THEN
            RAISE EXCEPTION 'tp.wrk.min must not be larger than tp.wrk.max on page %', NEW.page USING ERRCODE = '22023';
        END IF;
    END IF;

    RETURN NEW;
END;
$fn$;

-- shallow JSON diff used by the audit log
CREATE OR REPLACE FUNCTION pool_config_jsonb_diff(p_before jsonb, p_after jsonb)
RETURNS jsonb
LANGUAGE sql
IMMUTABLE
AS $fn$
    SELECT COALESCE(
        jsonb_object_agg(
            k,
            jsonb_build_object(
                'from', COALESCE(p_before -> k, 'null'::jsonb),
                'to',   COALESCE(p_after  -> k, 'null'::jsonb)
            )
        ),
        '{}'::jsonb
    )
    FROM (
        SELECT key AS k FROM jsonb_object_keys(COALESCE(p_before, '{}'::jsonb)) AS b(key)
        UNION
        SELECT key AS k FROM jsonb_object_keys(COALESCE(p_after,  '{}'::jsonb)) AS a(key)
    ) AS keys
    WHERE COALESCE(p_before -> k, 'null'::jsonb) IS DISTINCT FROM COALESCE(p_after -> k, 'null'::jsonb);
$fn$;

-- write the audit row of every new version
-- WO FIX-E: the actor is the author of the row (NEW.author_id). It was read
-- from the session variable app.actor_id before, and that variable is
-- settable by any session: any client could write any actor into the
-- security log, and a non-numeric value of it killed every save with 22P02.
CREATE OR REPLACE FUNCTION pool_config_audit_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_prev  pool_config_version;
BEGIN
    SELECT v.* INTO v_prev FROM pool_config_version v
     WHERE v.page = NEW.page AND v.version < NEW.version
     ORDER BY v.version DESC LIMIT 1;

    INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
    VALUES (NEW.author_id,
            'config_version_saved',
            NEW.page,
            v_prev.version,
            NEW.version,
            pool_config_jsonb_diff(COALESCE(v_prev.config, '{}'::jsonb), NEW.config));

    RETURN NULL;
END;
$fn$;

-- write the audit row of every change of a config version
-- WO FIX-E: an UPDATE of a config version is audited too, so a change of the
-- history cannot pass without a trace. The pin and the unpin are audited by
-- their own function with the admin who asked for it; the change of the
-- in_use flag is a system action.
CREATE OR REPLACE FUNCTION pool_config_audit_update()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_action text;
    v_actor  bigint;
BEGIN
    IF NEW.pinned_name IS DISTINCT FROM OLD.pinned_name THEN
        -- the pin and the unpin write their own audit row
        RETURN NULL;
    END IF;

    IF NEW.in_use IS DISTINCT FROM OLD.in_use THEN
        v_action := 'config_version_in_use_changed';
        v_actor  := NULL;
    ELSE
        v_action := 'config_version_updated';
        v_actor  := NEW.author_id;
    END IF;

    INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
    VALUES (v_actor, v_action, NEW.page, OLD.version, NEW.version,
            pool_config_jsonb_diff(OLD.config, NEW.config));

    RETURN NULL;
END;
$fn$;

-- the security log is append-only: no UPDATE, no DELETE, ever
CREATE OR REPLACE FUNCTION security_audit_log_append_only()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    RAISE EXCEPTION 'security_audit_log is append-only: % is not allowed', TG_OP USING ERRCODE = '42501';
END;
$fn$;

-- WO FIX-E: TRUNCATE was not covered before, so the owner could wipe the
-- "never deleted" log in one statement. It is refused now, and TRUNCATE is
-- revoked as well.
CREATE OR REPLACE FUNCTION security_audit_log_no_truncate()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
    RAISE EXCEPTION 'security_audit_log is append-only: TRUNCATE is not allowed' USING ERRCODE = '42501';
END;
$fn$;

-- WO FIX-E: the audit row of a pruned version is written by pool_config_prune()
-- with the admin who asked for the prune. The old trigger read the actor from
-- the client-settable session variable, so every pruned version had a null
-- actor, and the cleanup of the test suite wrote rows of its own.
DROP TRIGGER IF EXISTS pool_config_version_audit_delete_trg ON pool_config_version;

CREATE TRIGGER pool_config_version_number_trg BEFORE INSERT ON pool_config_version
    FOR EACH ROW EXECUTE FUNCTION pool_config_assign_version();

CREATE TRIGGER pool_config_version_guard_trg BEFORE INSERT OR UPDATE ON pool_config_version
    FOR EACH ROW EXECUTE FUNCTION pool_config_guard_version();

CREATE TRIGGER pool_config_version_audit_trg AFTER INSERT ON pool_config_version
    FOR EACH ROW EXECUTE FUNCTION pool_config_audit_insert();

CREATE TRIGGER pool_config_version_audit_update_trg AFTER UPDATE ON pool_config_version
    FOR EACH ROW EXECUTE FUNCTION pool_config_audit_update();

CREATE TRIGGER security_audit_log_append_only_trg BEFORE UPDATE OR DELETE ON security_audit_log
    FOR EACH ROW EXECUTE FUNCTION security_audit_log_append_only();

CREATE TRIGGER security_audit_log_no_truncate_trg BEFORE TRUNCATE ON security_audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION security_audit_log_no_truncate();

-- WO FIX-E: no UPDATE, no DELETE and no TRUNCATE for anybody but the owner.
REVOKE UPDATE, DELETE, TRUNCATE ON security_audit_log FROM PUBLIC;

-- ---------------------------------------------------------------------
-- 9. Save, pin, rollback and prune (WWPP-R38.6, R38.7)
-- ---------------------------------------------------------------------

-- Save: create a new version of one page, audited, with the schema check
-- WO FIX-E: the lock of the page is taken, so the version number stays dense
-- and a prune of the same page cannot run at the same time.
CREATE OR REPLACE FUNCTION pool_config_save(
    p_page            text,
    p_config          jsonb,
    p_reason          text,
    p_author_id       bigint,
    p_json_schema_ver text    DEFAULT NULL,
    p_pinned_name     text    DEFAULT NULL
)
RETURNS pool_config_version
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_row       pool_config_version;
    v_schema_ver text;
BEGIN
    IF p_author_id IS NULL OR NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;
    IF btrim(COALESCE(p_reason, '')) = '' THEN
        RAISE EXCEPTION 'every Save needs a reason (who, when, what, why)' USING ERRCODE = '22023';
    END IF;
    IF p_pinned_name IS NOT NULL AND char_length(btrim(p_pinned_name)) NOT BETWEEN 1 AND 64 THEN
        RAISE EXCEPTION 'a pin name is 1 to 64 characters' USING ERRCODE = '22023';
    END IF;

    PERFORM pool_config_lock_page(p_page);

    SELECT s.schema_version INTO v_schema_ver FROM pool_config_json_schema s
     WHERE s.page = p_page AND s.is_current;

    INSERT INTO pool_config_version (page, version, config, json_schema_ver, author_id, reason, pinned_name)
    VALUES (p_page, 0, p_config, COALESCE(p_json_schema_ver, v_schema_ver, 'unversioned'), p_author_id, p_reason, p_pinned_name)
    RETURNING * INTO v_row;

    RETURN v_row;
END;
$fn$;

-- Pin: name a version so pruning never removes it (WWPP-R38.7)
-- WO FIX-E: the admin must be active, the name is at most 64 characters, and
-- a version that is already pinned under another name is refused instead of
-- overwriting the old pin silently.
CREATE OR REPLACE FUNCTION pool_config_pin(
    p_page      text,
    p_version   integer,
    p_pin_name  text,
    p_author_id bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_old_name text;
BEGIN
    IF p_author_id IS NULL OR NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;
    IF btrim(COALESCE(p_pin_name, '')) = '' THEN
        RAISE EXCEPTION 'a pin needs a name' USING ERRCODE = '22023';
    END IF;
    IF char_length(btrim(p_pin_name)) > 64 THEN
        RAISE EXCEPTION 'a pin name is at most 64 characters' USING ERRCODE = '22023';
    END IF;

    PERFORM pool_config_lock_page(p_page);

    SELECT v.pinned_name INTO v_old_name FROM pool_config_version v
     WHERE v.page = p_page AND v.version = p_version FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'version % of page % does not exist', p_version, p_page USING ERRCODE = '22023';
    END IF;
    IF v_old_name IS NOT NULL AND v_old_name <> p_pin_name THEN
        RAISE EXCEPTION 'version % of page % is already pinned as %', p_version, p_page, v_old_name USING ERRCODE = '22023';
    END IF;

    UPDATE pool_config_version v
       SET pinned_name = p_pin_name
     WHERE v.page = p_page AND v.version = p_version;

    INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
    VALUES (p_author_id, 'config_version_pinned', p_page, p_version, p_version,
            jsonb_build_object('pinned_name', p_pin_name));
END;
$fn$;

-- Unpin: release a pinned version back to normal pruning
-- WO FIX-E: only a row that is really pinned is released, so the log never
-- claims an unpin that did not happen.
CREATE OR REPLACE FUNCTION pool_config_unpin(
    p_page      text,
    p_version   integer,
    p_author_id bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
BEGIN
    IF p_author_id IS NULL OR NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;

    PERFORM pool_config_lock_page(p_page);

    UPDATE pool_config_version v
       SET pinned_name = NULL
     WHERE v.page = p_page AND v.version = p_version AND v.pinned_name IS NOT NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'version % of page % is not pinned', p_version, p_page USING ERRCODE = '22023';
    END IF;

    INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
    VALUES (p_author_id, 'config_version_unpinned', p_page, p_version, p_version, '{}'::jsonb);
END;
$fn$;

-- Rollback: copy an earlier version into a NEW audited version
-- (WWPP-R38.6 - the rolled-back copy is a new version, the old ones stay)
-- WO FIX-E: the rollback is a Save, so the old config is validated against
-- the schema that is registered NOW and the new row carries that schema
-- version. Before, it copied the old json_schema_ver, and the guard demands
-- the registered one, so every rollback raised 22023 after a schema bump.
CREATE OR REPLACE FUNCTION pool_config_rollback(
    p_page      text,
    p_to_version integer,
    p_reason    text,
    p_author_id bigint
)
RETURNS pool_config_version
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_src        pool_config_version;
    v_new        pool_config_version;
    v_schema_ver text;
BEGIN
    IF p_author_id IS NULL OR NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;
    -- the reason is checked BEFORE the text is built, so the prefix
    -- "rollback to version N: " cannot pass an empty reason
    IF btrim(COALESCE(p_reason, '')) = '' THEN
        RAISE EXCEPTION 'every rollback needs a reason (who, when, what, why)' USING ERRCODE = '22023';
    END IF;

    PERFORM pool_config_lock_page(p_page);

    SELECT v.* INTO v_src FROM pool_config_version v
     WHERE v.page = p_page AND v.version = p_to_version;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'version % of page % does not exist', p_to_version, p_page USING ERRCODE = '22023';
    END IF;

    SELECT s.schema_version INTO v_schema_ver FROM pool_config_json_schema s
     WHERE s.page = p_page AND s.is_current;
    IF v_schema_ver IS NULL THEN
        RAISE EXCEPTION 'no JSON Schema is registered for page %', p_page USING ERRCODE = '22023';
    END IF;

    -- the old config must still satisfy the schema of today
    PERFORM pool_config_validate_schema(p_page, v_src.config);

    INSERT INTO pool_config_version (page, version, config, json_schema_ver, author_id, reason, pinned_name)
    VALUES (p_page, 0, v_src.config, v_schema_ver, p_author_id,
            'rollback to version ' || p_to_version || ': ' || COALESCE(p_reason, ''), NULL)
    RETURNING * INTO v_new;

    RETURN v_new;
END;
$fn$;

-- Retention: change how many versions a page keeps (WWPP-R38.7)
CREATE OR REPLACE FUNCTION pool_config_set_retention(
    p_page      text,
    p_keep_last integer,
    p_author_id bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_old integer;
BEGIN
    IF p_author_id IS NULL OR NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;
    IF p_keep_last < 1 THEN
        RAISE EXCEPTION 'keep_last must be at least 1' USING ERRCODE = '22023';
    END IF;

    SELECT r.keep_last INTO v_old FROM pool_config_retention r WHERE r.page = p_page;

    INSERT INTO pool_config_retention (page, keep_last)
    VALUES (p_page, p_keep_last)
    ON CONFLICT (page) DO UPDATE SET keep_last = EXCLUDED.keep_last;

    INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
    VALUES (p_author_id, 'retention_changed', p_page, NULL, NULL,
            jsonb_build_object('from', v_old, 'to', p_keep_last));
END;
$fn$;

-- Prune: delete the versions of a page that are beyond keep_last, are not
-- pinned and are not used by a running job.  Audit rows are never deleted.
-- WO FIX-E: the admin who asked for the prune is the actor of the audit
-- rows (before, every pruned version had a null actor), the lock of the page
-- is taken (so an attach of the same page cannot race with the prune), and
-- the retention counts only the FREE versions: a pinned version and a
-- version a running job uses are kept IN ADDITION (WWPP-R38.7).
CREATE OR REPLACE FUNCTION pool_config_prune(p_page text DEFAULT NULL, p_author_id bigint DEFAULT NULL)
RETURNS TABLE (page text, deleted_versions integer, kept_versions integer)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_page   text;
    v_keep   integer;
    v_deleted integer;
    v_kept   integer;
BEGIN
    IF p_author_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM admin_user u WHERE u.id = p_author_id AND u.is_active) THEN
        RAISE EXCEPTION 'author % is not an active admin_user', p_author_id USING ERRCODE = '22023';
    END IF;

    FOR v_page IN
        SELECT p.id FROM pool_admin_page p
         WHERE p_page IS NULL OR p.id = p_page
         ORDER BY p.id
    LOOP
        PERFORM pool_config_lock_page(v_page);

        SELECT COALESCE(r.keep_last, 10) INTO v_keep FROM pool_config_retention r WHERE r.page = v_page;
        IF v_keep IS NULL THEN
            v_keep := 10;
        END IF;

        WITH free AS (
            SELECT v.id FROM pool_config_version v
             WHERE v.page = v_page
               AND v.pinned_name IS NULL
               AND NOT EXISTS (SELECT 1 FROM job_config_ref j WHERE j.config_version_id = v.id)
             ORDER BY v.version DESC, v.id DESC
             LIMIT v_keep
        ), doomed AS (
            SELECT v.id FROM pool_config_version v
             WHERE v.page = v_page
               AND v.pinned_name IS NULL
               AND NOT EXISTS (SELECT 1 FROM job_config_ref j WHERE j.config_version_id = v.id)
               AND NOT EXISTS (SELECT 1 FROM free f WHERE f.id = v.id)
        ), removed AS (
            DELETE FROM pool_config_version v USING doomed d WHERE v.id = d.id
            RETURNING v.id, v.page, v.version, v.config
        ), logged AS (
            INSERT INTO security_audit_log (actor_id, action, page, from_version, to_version, diff)
            SELECT p_author_id, 'config_version_pruned', r.page, r.version, NULL,
                   jsonb_build_object('removed', r.config)
              FROM removed r
            RETURNING 1
        )
        SELECT count(*) INTO v_deleted FROM removed;

        SELECT count(*) INTO v_kept FROM pool_config_version v WHERE v.page = v_page;

        page            := v_page;
        deleted_versions := v_deleted;
        kept_versions    := v_kept;
        RETURN NEXT;
    END LOOP;
END;
$fn$;

-- Attach: pin a job to a config version for the whole life of the job
-- (WWPP-R38.6). The lock of the page is taken, so a prune of the same page
-- cannot delete the version between the read and the write.
CREATE OR REPLACE FUNCTION pool_config_attach_job(p_job_id text, p_page text, p_version integer)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_id bigint;
BEGIN
    PERFORM pool_config_lock_page(p_page);

    SELECT v.id INTO v_id FROM pool_config_version v
     WHERE v.page = p_page AND v.version = p_version;
    IF v_id IS NULL THEN
        RETURN false;
    END IF;

    INSERT INTO job_config_ref (job_id, config_version_id)
    VALUES (p_job_id, v_id)
    ON CONFLICT (job_id) DO UPDATE SET config_version_id = EXCLUDED.config_version_id;

    RETURN true;
END;
$fn$;

-- Attach the newest version of a page to a job in ONE call. The version is
-- read and pinned under the same lock, so the prune cannot delete the
-- version between the two steps (the Latest -> AttachJob race of the review).
CREATE OR REPLACE FUNCTION pool_config_attach_job_latest(p_job_id text, p_page text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_id bigint;
BEGIN
    PERFORM pool_config_lock_page(p_page);

    SELECT v.id INTO v_id FROM pool_config_version v
     WHERE v.page = p_page
     ORDER BY v.version DESC, v.id DESC
     LIMIT 1;
    IF v_id IS NULL THEN
        RETURN false;
    END IF;

    INSERT INTO job_config_ref (job_id, config_version_id)
    VALUES (p_job_id, v_id)
    ON CONFLICT (job_id) DO UPDATE SET config_version_id = EXCLUDED.config_version_id;

    RETURN true;
END;
$fn$;

-- Detach: release the config version a job held. The lock of the page of the
-- job is taken, so a prune of that page cannot run at the same time.
CREATE OR REPLACE FUNCTION pool_config_detach_job(p_job_id text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_page text;
BEGIN
    SELECT v.page INTO v_page
      FROM job_config_ref r
      JOIN pool_config_version v ON v.id = r.config_version_id
     WHERE r.job_id = p_job_id;
    IF v_page IS NULL THEN
        RETURN false;
    END IF;

    PERFORM pool_config_lock_page(v_page);

    DELETE FROM job_config_ref WHERE job_id = p_job_id;

    RETURN true;
END;
$fn$;

-- ---------------------------------------------------------------------
-- 10. Views: current version and the in_use flag (WWPP-R40.2, R38.7)
-- ---------------------------------------------------------------------

-- WO FIX-E: the in_use flag is DERIVED from job_config_ref, so it can never
-- be stuck: two concurrent detaches of the last two jobs of one version
-- cannot leave the flag true (the READ COMMITTED write skew of the review),
-- and the flag is never false while a job runs.
-- The current version is computed with a window function instead of a
-- correlated max() per row, and the view runs as the invoker, so the row
-- level security of the base tables applies to it as well.
CREATE VIEW pool_config_version_state WITH (security_invoker = true) AS
SELECT v.id,
       v.page,
       v.version,
       v.config,
       v.json_schema_ver,
       v.author_id,
       v.reason,
       v.created_at,
       v.pinned_name,
       (v.pinned_name IS NOT NULL) AS pinned,
       EXISTS (SELECT 1 FROM job_config_ref j WHERE j.config_version_id = v.id) AS in_use,
       (v.version = max(v.version) OVER (PARTITION BY v.page)) AS is_current
  FROM pool_config_version v;

COMMENT ON VIEW pool_config_version_state IS 'Every config version with its flags: pinned, in_use (a running job keeps this version) and is_current (the version new jobs take now). The in_use flag is derived from job_config_ref, so it is always the truth.';

-- The application reads these tables and calls the functions of this script;
-- it writes nothing itself. The grants below are the ones the deployment
-- needs; the role name is a setting of the deployment.
GRANT SELECT ON pool_admin_page, pool_config_version, pool_config_retention,
                pool_config_json_schema, job_config_ref, pool_config_version_state TO PUBLIC;

-- ---------------------------------------------------------------------
-- 11. Seed rows: the 7 pages and their retention (WWPP-R38.7)

INSERT INTO pool_admin_page (id, tier, title, html_file, manual_path, sort_order) VALUES
    ('monitor',              1, 'Combined brief Monitor',      'monitor.html',              '/pools/manual/monitor.html',              1),
    ('pool-monitor',         2, 'Pool Monitor',                'pool-monitor.html',         '/pools/manual/pool-monitor.html',         2),
    ('api-monitor',          2, 'API Monitor',                 'api-monitor.html',          '/pools/manual/api-monitor.html',          3),
    ('thread-pool-admin',    3, 'Thread Pool admin',           'thread-pool-admin.html',    '/pools/manual/thread-pool-admin.html',    4),
    ('process-pool-admin',   3, 'Process Pool admin',          'process-pool-admin.html',   '/pools/manual/process-pool-admin.html',   5),
    ('thread-api-admin',     3, 'Thread Pool API admin',       'thread-api-admin.html',     '/pools/manual/thread-api-admin.html',     6),
    ('process-api-admin',    3, 'Process Pool API admin',      'process-api-admin.html',    '/pools/manual/process-api-admin.html',    7)
ON CONFLICT (id) DO NOTHING;

INSERT INTO pool_config_retention (page, keep_last)
SELECT p.id, 10 FROM pool_admin_page p
ON CONFLICT (page) DO NOTHING;

-- ---------------------------------------------------------------------
-- 12. The registered JSON Schema of every admin page (schema_version 1.0.0)
--     WO FIX-E: before, only thread-pool-admin had a registered document,
--     so the other 6 pages could never be saved (the app refuses a Save
--     whose page has no schema: ErrInvalidConfig). The 6 documents below
--     are built from docs/ADMIN_MANUALS_SOURCE.md, the one markdown source
--     of the 7 manual pages; the 7th is the thread-pool-admin document,
--     identical to pool_config.schema.json.
--     The table keeps the history of the documents: the primary key is
--     (page, schema_version) and exactly one document per page is current.
-- ---------------------------------------------------------------------

-- the thread-pool-admin document (identical to pool_config.schema.json, version 1.0.0)
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('thread-pool-admin', 'thread-pool-admin', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/thread-pool-admin/1.0.0",
  "title": "CCGSC thread-pool-admin page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'thread-pool-admin'. Covers the re-run percentage, the 4 tamper measures with their parameters, the workers-per-tab min/max/auto settings, P2P enabled (default true), dew enabled with its window in seconds, the metrics on/off switches and keep_last. This document is the authority: every Save is validated against it (WWPP-R40.2). The matching PostgreSQL DDL is pool_config.sql.",
  "$comment": "Header: WO-HC-SCH-01 (hc, vostro, cmdc deepseek-v4-pro) - config storage schema, tmp only. Work order author: Claude Opus 5.5 head, 2026-11-17. Implemented by hermes MiniMax-M3, 2026-11-17. Truth input: F:/tmp/hcwork/ctx_rows_R37_R40.md rows WWPP-R38.6, WWPP-R38.7, WWPP-R40.2 and owner rule R-97. Status: tmp only, draft - not deployed, no database was contacted. Guardrails honoured: only this file and pool_config.sql were written; no DB connection; no network. END footer: the last key is x_end.",
  "x_header": {
    "work_order": "WO-HC-SCH-01",
    "page": "thread-pool-admin",
    "schema_id": "thread-pool-admin",
    "schema_version": "1.0.0",
    "truth_inputs": [
      "WWPP-R38.6",
      "WWPP-R38.7",
      "WWPP-R40.2",
      "R-97"
    ],
    "companion_ddl": "pool_config.sql",
    "status": "tmp only, draft, not deployed",
    "created": "2026-11-17",
    "created_by": "hermes MiniMax-M3",
    "units": {
      "tp.dew.window_seconds": "seconds (86400 = 24 h)",
      "cfg.keep_last": "versions kept per page",
      "tp.ver.rerun_percent": "percent (0-100, 0 = off)"
    }
  },
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^(tp|cfg|redis)\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "tp.wrk.per_tab",
    "tp.wrk.min",
    "tp.wrk.max",
    "tp.ver.hash_check",
    "tp.ver.rerun_percent",
    "tp.ver.signed_envelope",
    "tp.ver.canary",
    "tp.p2p.enabled",
    "tp.dew.enabled",
    "tp.dew.window_seconds",
    "tp.met.light",
    "cfg.keep_last"
  ],
  "properties": {
    "tp.gen.enabled": {
      "type": "boolean",
      "default": true,
      "description": "Master switch; off stops new task leasing in this pool. (WWPP-R27.1)"
    },
    "tp.gen.name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 64,
      "default": "Web Thread Pool",
      "description": "Display name shown in nav, monitors and API responses. (WWPP-R38.5)"
    },
    "tp.gen.fetch_mode": {
      "type": "string",
      "enum": [
        "leader",
        "each-tab"
      ],
      "default": "leader",
      "description": "Same-device fetch mode: leader tab per device or every tab on its own; inefficient pool use always warns. (WWPP-R22.1, WWPP-R23.2)"
    },
    "tp.gen.shareable": {
      "type": "boolean",
      "default": true,
      "description": "Lets a developer mark outputs shareable; the admin may disable it for the pool. (WWPP-R23.4)"
    },
    "tp.gen.group_by": {
      "type": "string",
      "enum": [
        "none",
        "label",
        "device-class"
      ],
      "default": "none",
      "description": "Default task partitioning key applied when the developer gives none. (WWPP-R32.2)"
    },
    "tp.wrk.per_tab": {
      "oneOf": [
        {
          "const": "auto"
        },
        {
          "type": "integer",
          "minimum": 1,
          "maximum": 4
        }
      ],
      "default": "auto",
      "description": "Compute web worker threads per tab; auto means logical cores minus one, otherwise 1 to 4. (WWPP-R29.4)"
    },
    "tp.wrk.min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "default": 1,
      "description": "Lowest number of compute workers the pool allows per tab. (WWPP-R29.4)"
    },
    "tp.wrk.max": {
      "type": "integer",
      "minimum": 1,
      "maximum": 32,
      "default": 4,
      "description": "Highest number of compute workers per tab; the server admin may raise it where more cores exist. (WWPP-R29.4, WWPP-R23.1)"
    },
    "tp.wrk.phones": {
      "type": "integer",
      "minimum": 1,
      "maximum": 2,
      "default": 1,
      "description": "Workers per tab on phones. (WWPP-R10.4)"
    },
    "tp.wrk.laptops": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "default": 2,
      "description": "Workers per tab on laptops. (WWPP-R10.4)"
    },
    "tp.wrk.desktops": {
      "type": "integer",
      "minimum": 1,
      "maximum": 8,
      "default": 4,
      "description": "Workers per tab on desktops. (WWPP-R10.4)"
    },
    "tp.wrk.owner": {
      "oneOf": [
        {
          "const": "auto"
        },
        {
          "type": "integer",
          "minimum": 1,
          "maximum": 32
        }
      ],
      "default": "auto",
      "description": "Workers per tab on owner nodes. (WWPP-R10.4, WWPP-R29.4)"
    },
    "tp.wrk.phone_battery": {
      "type": "integer",
      "minimum": 1,
      "maximum": 2,
      "default": 1,
      "description": "Workers per tab while the phone runs on battery. (WWPP-R30.2)"
    },
    "tp.wrk.phone_charging": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "default": 2,
      "description": "Workers per tab while charging with the screen on. (WWPP-R30.2)"
    },
    "tp.wrk.phone_stop": {
      "type": "integer",
      "minimum": 5,
      "maximum": 50,
      "default": 20,
      "description": "Work stops below this battery level. (WWPP-R30.2)"
    },
    "tp.gd.cpu": {
      "type": "string",
      "enum": [
        "low",
        "medium",
        "full"
      ],
      "default": "medium",
      "description": "Contributor CPU effort profile the server asks for. (WWPP-R30.1)"
    },
    "tp.gd.backoff": {
      "type": "boolean",
      "default": true,
      "description": "Drops worker effort only when the device is very very busy. (WWPP-R30.1)"
    },
    "tp.gd.battery": {
      "type": "boolean",
      "default": true,
      "description": "Applies the phone battery and heat rules where the device supports them. (WWPP-R30.1)"
    },
    "tp.gd.memory": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4096,
      "description": "Upper memory bound asked of each worker, in MiB. Default TBD: the owner has not decided it. (WWPP-R30.1)"
    },
    "tp.gd.hidden_tab": {
      "type": "boolean",
      "const": false,
      "description": "Deliberately off: hidden and background tabs must keep working, never pause. (WWPP-R30.1, WWPP-R39.1)"
    },
    "tp.gpu.enabled": {
      "type": "boolean",
      "default": true,
      "description": "Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool. (WWPP-R33.1)"
    },
    "tp.gpu.time_limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 3600,
      "description": "Maximum runtime of one GPU task, in seconds. Default TBD: the owner has not decided it. (WWPP-R33.1)"
    },
    "tp.gpu.memory_limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 8192,
      "description": "Maximum GPU memory one task may request, in MiB. Default TBD: the owner has not decided it. (WWPP-R33.1)"
    },
    "tp.gpu.charging_only": {
      "type": "boolean",
      "default": true,
      "description": "GPU tasks are eligible only while the device is charging. (WWPP-R33.1)"
    },
    "tp.sch.bounded_queue": {
      "type": "boolean",
      "default": true,
      "description": "Caps the admitted queue depth; the queue never grows without limit. (WWPP-R32.5)"
    },
    "tp.sch.rejection": {
      "type": "string",
      "enum": [
        "reject-429",
        "block-timeout",
        "drop-oldest",
        "caller-runs"
      ],
      "description": "What happens when the bounded queue is full. Default TBD: the owner has not decided it; reject-429 is the proposal. (WWPP-R25.3)"
    },
    "tp.sch.priority_high": {
      "type": "boolean",
      "default": false,
      "description": "Lets the admin allow the high priority class in this pool. (WWPP-R25.2)"
    },
    "tp.sch.fair_share": {
      "type": "boolean",
      "default": true,
      "description": "Shares pool capacity fairly between developers. (WWPP-R32.5)"
    },
    "tp.sch.batch_size": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1000,
      "description": "Default task batch size handed to a device class. Default TBD: the owner has not decided it. (WWPP-R34.1)"
    },
    "tp.sch.small_task": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Tasks below this size raise an efficiency warning on all four surfaces. Default TBD: the owner has not decided it. (WWPP-R24.4)"
    },
    "tp.ver.hash_check": {
      "type": "boolean",
      "default": true,
      "description": "Tamper measure 1 of 4: verifies artifact and input SHA-256 on every load, including IndexedDB cache hits. (WWPP-R37.1, WWPP-R37.1b)"
    },
    "tp.ver.rerun_percent": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "default": 1,
      "description": "Tamper measure 2 of 4: percentage of accepted results re-run on another worker, exact hashes only; 0 % turns the measure off. Developer-led inside the admin allowance. (WWPP-R35.1, WWPP-R37.1b)"
    },
    "tp.ver.signed_envelope": {
      "type": "boolean",
      "default": true,
      "description": "Tamper measure 3 of 4: server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease. (WWPP-R37.1, WWPP-R37.1b)"
    },
    "tp.ver.canary": {
      "type": "boolean",
      "default": true,
      "description": "Tamper measure 4 of 4: schedules hidden known-answer tasks to test worker trustworthiness. (WWPP-R37.1, WWPP-R37.1b)"
    },
    "tp.ver.canary_warn": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "default": 75,
      "description": "Reputation score at or below which the worker is warned. (WWPP-R37.1, WWPP-R39.1)"
    },
    "tp.ver.canary_bar": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "default": 50,
      "description": "Reputation score at or below which the worker is barred from tasks. (WWPP-R37.1, WWPP-R39.1)"
    },
    "tp.ver.canary_ban": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "default": 0,
      "description": "Reputation score at or below which the worker is banned. (WWPP-R37.1, WWPP-R39.1)"
    },
    "tp.ver.reputation_recovery": {
      "type": "string",
      "enum": [
        "manual-review",
        "auto"
      ],
      "default": "manual-review",
      "description": "A ban is lifted by admin review; there is no automatic recovery. (WWPP-R37.1, WWPP-R39.1)"
    },
    "tp.ver.mode": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "default": "a",
      "description": "(a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified. (WWPP-R24.1)"
    },
    "tp.ver.k_of_n": {
      "type": "integer",
      "minimum": 1,
      "maximum": 5,
      "description": "Independent checks that must agree before a result counts as verified. Default TBD: the owner has not decided it. (WWPP-R24.1)"
    },
    "tp.ver.local_after_download": {
      "type": "boolean",
      "default": true,
      "description": "Lets developers verify results locally after download. (WWPP-R24.1)"
    },
    "tp.ver.evidence": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "default": "a",
      "description": "(a) server minimal: security log plus SHA-256 of every accepted result, or (b) everything opt-in. (WWPP-R35.2)"
    },
    "tp.ver.chain": {
      "type": "boolean",
      "default": false,
      "description": "Per-job hash chain written by the server; built and tested even while off. (WWPP-R26.2)"
    },
    "tp.ver.merkle": {
      "type": "boolean",
      "default": false,
      "description": "Adds periodic Merkle roots over the tips of job chains. (WWPP-R22.3)"
    },
    "tp.ver.chain_authority": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance. Default TBD: the owner has not decided it. (WWPP-R23.3)"
    },
    "tp.p2p.enabled": {
      "type": "boolean",
      "default": true,
      "description": "Hash-addressed sharing of server-published artifacts and inputs; results always go to the server. Default ON everywhere until tested; the admin may switch it off. (WWPP-R36.2, WWPP-R36.2-RES)"
    },
    "tp.p2p.peer_scope": {
      "type": "string",
      "enum": [
        "same-device",
        "same-phone",
        "same-lan",
        "near"
      ],
      "default": "same-lan",
      "description": "Which peers may serve artifacts: same device, same phone, same LAN or very close proximity. (WWPP-R9.1)"
    },
    "tp.p2p.task_handoff": {
      "type": "boolean",
      "default": false,
      "description": "Historical P2P scope kept as a setting; not an active peer data flow. (WWPP-R9.1, WWPP-R36.2)"
    },
    "tp.p2p.result_relay": {
      "type": "boolean",
      "default": false,
      "description": "Historical P2P scope kept as a setting; results are never relayed over P2P. (WWPP-R9.1, WWPP-R36.2, WWPP-R39.1)"
    },
    "tp.p2p.rtt_probe": {
      "type": "boolean",
      "default": false,
      "description": "Uses round-trip time in peer ordering once the probe exists. (WWPP-R14.1)"
    },
    "tp.p2p.fallback": {
      "type": "boolean",
      "default": true,
      "description": "Falls back to server or CDN when no peer can serve the artifact. (WWPP-R36.2)"
    },
    "tp.dew.enabled": {
      "type": "boolean",
      "default": true,
      "description": "Offline leases with signed receipts so a tab may submit results later. (WWPP-R36.2)"
    },
    "tp.dew.window_seconds": {
      "type": "integer",
      "minimum": 3600,
      "maximum": 259200,
      "default": 86400,
      "description": "How long a signed dew receipt stays valid, in seconds; the default 86400 s is the 24 h window. (WWPP-R36.2)"
    },
    "tp.dew.late_policy": {
      "type": "string",
      "enum": [
        "a",
        "b",
        "c"
      ],
      "default": "a",
      "description": "(a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease. (WWPP-R13.2)"
    },
    "tp.dew.max_attempts": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "default": 3,
      "description": "Attempts allowed for one task before it fails. (WWPP-R32.9)"
    },
    "tp.dew.job_policy": {
      "type": "string",
      "enum": [
        "partial",
        "fail-fast",
        "threshold"
      ],
      "default": "partial",
      "description": "What the job does when a task cannot complete. (WWPP-R32.9)"
    },
    "tp.dew.quarantine": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "default": 3,
      "description": "Worker crashes before a poison task is quarantined. (WWPP-R32.9)"
    },
    "tp.dew.dead_letter": {
      "type": "boolean",
      "default": true,
      "description": "Keeps late results that arrive after the job ended, with no second credit. (WWPP-R32.9)"
    },
    "tp.met.light": {
      "type": "boolean",
      "default": true,
      "description": "Light metrics: per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated. (WWPP-R37.2)"
    },
    "tp.met.detail": {
      "type": "string",
      "enum": [
        "light",
        "standard",
        "full"
      ],
      "description": "How much per-feature detail the metrics table keeps. Default TBD: the owner has not decided it. (WWPP-R37.2)"
    },
    "tp.met.benchmark": {
      "type": "boolean",
      "default": true,
      "description": "Heavy thesis benchmark harness for owner-started thesis runs beside the MVP. (WWPP-R37.2)"
    },
    "cfg.keep_last": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "default": 10,
      "description": "Number of config versions kept for this admin page; the count is itself an admin setting. Mirrored in pool_config_retention.keep_last. (WWPP-R38.7)"
    },
    "cfg.reason_required": {
      "type": "boolean",
      "default": true,
      "description": "Each Save records who, when, what changed and why. (WWPP-R38.6)"
    },
    "cfg.pin_name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 64,
      "description": "Optional admin-named pin, for example a thesis run; the name is unique per page. (WWPP-R38.7)"
    },
    "redis.memory.mode": {
      "type": "string",
      "enum": [
        "fixed",
        "dynamic"
      ],
      "default": "fixed",
      "description": "T91 / R42.8: shared Redis cap mode; always noeviction."
    },
    "redis.memory.fixed_gb": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "default": 1,
      "description": "Fixed cap in binary GB (GiB)."
    },
    "redis.memory.dynamic_percent": {
      "type": "integer",
      "minimum": 10,
      "maximum": 40,
      "default": 10,
      "description": "Percent of Linux MemAvailable, clamped to 1-4 GiB."
    },
    "redis.memory.interval_min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 60,
      "default": 5,
      "description": "Dynamic recomputation interval in minutes."
    },
    "redis.persistence.mode": {
      "type": "string",
      "enum": [
        "aof_everysec",
        "snapshot"
      ],
      "default": "aof_everysec",
      "description": "AOF every second or existing Redis snapshot save rules only."
    }
  },
  "x_notes": [
    "tp.wrk.min must not exceed tp.wrk.max: JSON Schema 2020-12 cannot compare two sibling numbers, so this cross-field rule is enforced by the pool_config_guard_version() trigger in pool_config.sql.",
    "The dew window is expressed in seconds (86400 s = the 24 h owner default) as the work order requires; pool_config.sql keeps the same unit in the DB check.",
    "A property without a default is a value the owner has not decided yet; it is marked TBD in its description and must be supplied explicitly on Save.",
    "The re-run measure is switched off with the value 0 %; it has no separate on/off switch, matching WWPP-R35.1.",
    "Defaults listed here are the proposal carried by the admin manual source ADMIN_MANUALS_SOURCE.md and the design draft; the owner keeps the final say."
  ],
  "x_id_mapping": {
    "note": "The admin manual source ADMIN_MANUALS_SOURCE.md is the UI list; this table maps the ids whose schema key differs from the manual id, so the app and the database agree on one name.",
    "tp.dew.window": "tp.dew.window_seconds - the manual shows hours, the schema stores seconds as the work order requires (default 86400 s = 24 h).",
    "tp.cfg.retention": "cfg.keep_last - the manual label is Version retention; the schema key is keep_last as the work order requires (default 10).",
    "tp.cfg.pin": "cfg.pin_name - the pin name itself lives in pool_config_version.pinned_name; this key carries the admin-named pin in the page config.",
    "tp.cfg.reason": "cfg.reason_required - the reason text lives in pool_config_version.reason; this key says whether a Save may omit it.",
    "tp.cfg.rollback": "not a stored value - rollback is the pool_config_rollback() function in pool_config.sql, it creates a new audited version."
  },
  "x_tamper_measures": {
    "1": "tp.ver.hash_check - hash check on every load, including IndexedDB cache hits (WWPP-R37.1)",
    "2": "tp.ver.rerun_percent - random re-run percentage, 0 % switches the measure off (WWPP-R35.1)",
    "3": "tp.ver.signed_envelope - Ed25519 signed task envelope (WWPP-R37.1)",
    "4": "tp.ver.canary - canary known-answer tasks plus the reputation thresholds (WWPP-R37.1)",
    "configurable_on": "the admin pages, on and off with parameters each (WWPP-R37.1b)"
  },
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the monitor document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('monitor', 'monitor', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/monitor/1.0.0",
  "title": "CCGSC monitor page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'monitor'. The settings are the ones of the admin manual page monitor.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^[a-z]{2,3}\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "mon.alert.auto",
    "mon.alert.level",
    "mon.alert.refresh",
    "mon.link.numbers",
    "mon.link.target",
    "mon.metrics.read",
    "mon.p95.window",
    "mon.trust.window_hours"
  ],
  "properties": {
    "mon.trust.window_hours": {
      "type": "integer",
      "minimum": 1,
      "maximum": 168,
      "description": "Rolling window used by every trust counter on this page"
    },
    "mon.alert.level": {
      "type": "string",
      "enum": [
        "info",
        "warn",
        "error"
      ],
      "description": "Lowest severity that still prints an error or warning line"
    },
    "mon.alert.refresh": {
      "type": "integer",
      "minimum": 5,
      "maximum": 300,
      "description": "Seconds between automatic refreshes of the brief numbers"
    },
    "mon.alert.auto": {
      "type": "boolean",
      "description": "Keeps the brief page current; off leaves the page manual"
    },
    "mon.link.numbers": {
      "type": "boolean",
      "description": "Every displayed number links to its matching settings section"
    },
    "mon.link.target": {
      "type": "string",
      "enum": [
        "same-tab",
        "new-tab"
      ],
      "description": "Where a linked number opens the matching section"
    },
    "mon.p95.window": {
      "type": "string",
      "enum": [
        "1m",
        "5m",
        "15m"
      ],
      "description": "Window used for the p95 latency number"
    },
    "mon.metrics.read": {
      "type": "boolean",
      "description": "The brief page reads the light metrics table and never writes measurements"
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the pool-monitor document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('pool-monitor', 'pool-monitor', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/pool-monitor/1.0.0",
  "title": "CCGSC pool-monitor page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'pool-monitor'. The settings are the ones of the admin manual page pool-monitor.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^[a-z]{2,3}\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "pm.cap.refresh",
    "pm.cap.slot_detail",
    "pm.ctl.pause_scheduling",
    "pm.ctl.requests",
    "pm.ctl.requeue_failed",
    "pm.dev.device_class",
    "pm.dev.gpu_reported",
    "pm.dev.workers_per_tab",
    "pm.err.banner",
    "pm.err.leases_expiring",
    "pm.work.window"
  ],
  "properties": {
    "pm.cap.refresh": {
      "type": "integer",
      "minimum": 5,
      "maximum": 300,
      "description": "Seconds between refreshes of the capacity dashboard"
    },
    "pm.cap.slot_detail": {
      "type": "boolean",
      "description": "Shows free and busy slots per pool, not only the totals"
    },
    "pm.work.window": {
      "type": "string",
      "enum": [
        "15m",
        "1h",
        "24h",
        "7d"
      ],
      "description": "Time window used by the work dashboard charts"
    },
    "pm.dev.workers_per_tab": {
      "type": "boolean",
      "description": "Shows the worker count each registered tab reports"
    },
    "pm.dev.device_class": {
      "type": "boolean",
      "description": "Groups tabs by phone, laptop, desktop or owner node"
    },
    "pm.dev.gpu_reported": {
      "type": "boolean",
      "description": "Shows tabs that report a usable WebGPU adapter"
    },
    "pm.err.banner": {
      "type": "string",
      "enum": [
        "compact",
        "large"
      ],
      "description": "Medium tier prints larger error messages than the brief page"
    },
    "pm.err.leases_expiring": {
      "type": "boolean",
      "description": "Shows leases close to expiry and their renewal state"
    },
    "pm.ctl.pause_scheduling": {
      "type": "boolean",
      "description": "Stops handing out new tasks; running jobs keep their leases"
    },
    "pm.ctl.requeue_failed": {
      "type": "boolean",
      "description": "Re-queues failed tasks that are still inside the retry policy"
    },
    "pm.ctl.requests": {
      "type": "boolean",
      "description": "Shows per-job developer requests filed against the pool defaults"
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the api-monitor document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('api-monitor', 'api-monitor', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/api-monitor/1.0.0",
  "title": "CCGSC api-monitor page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'api-monitor'. The settings are the ones of the admin manual page api-monitor.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^[a-z]{2,3}\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "am.ctl.pause_ingress",
    "am.err.banner",
    "am.err.codes",
    "am.keys.activity",
    "am.keys.revoked",
    "am.quota.credits",
    "am.quota.usage",
    "am.quota.warn",
    "am.scope",
    "am.sessions.list",
    "am.sys.card",
    "am.traffic.refresh",
    "am.traffic.window"
  ],
  "properties": {
    "am.scope": {
      "type": "string",
      "enum": [
        "thread",
        "process",
        "both"
      ],
      "description": "Which of the two pool APIs the monitor follows"
    },
    "am.traffic.refresh": {
      "type": "integer",
      "minimum": 5,
      "maximum": 300,
      "description": "Seconds between refreshes of the traffic cards"
    },
    "am.traffic.window": {
      "type": "string",
      "enum": [
        "1m",
        "5m",
        "15m"
      ],
      "description": "Window used for the latency card"
    },
    "am.err.banner": {
      "type": "string",
      "enum": [
        "compact",
        "large"
      ],
      "description": "Medium tier prints larger error messages than the brief page"
    },
    "am.err.codes": {
      "type": "boolean",
      "description": "Shows the RFC 9457 code list with counts per code"
    },
    "am.keys.activity": {
      "type": "boolean",
      "description": "Shows recent issuance, approval and revocation of API keys"
    },
    "am.keys.revoked": {
      "type": "boolean",
      "description": "Shows keys that are revoked and their deny-list state"
    },
    "am.quota.usage": {
      "type": "boolean",
      "description": "Shows per-key running-job, queued-task and request-per-minute usage"
    },
    "am.quota.warn": {
      "type": "boolean",
      "description": "Warns when a key approaches one of its quotas"
    },
    "am.quota.credits": {
      "type": "boolean",
      "description": "Shows metered CPU-seconds and GPU-seconds per key"
    },
    "am.sessions.list": {
      "type": "boolean",
      "description": "Shows process-pool sessions, leased tabs and expiry"
    },
    "am.sys.card": {
      "type": "boolean",
      "description": "Shows server CPU, RAM, disk, PG connections and SSE connections"
    },
    "am.ctl.pause_ingress": {
      "type": "boolean",
      "description": "Stops accepting new job submissions while live streams stay open"
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the process-pool-admin document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('process-pool-admin', 'process-pool-admin', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/process-pool-admin/1.0.0",
  "title": "CCGSC process-pool-admin page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'process-pool-admin'. The settings are the ones of the admin manual page process-pool-admin.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^([a-z]{2,3}|redis)\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "pp.cfg.reason",
    "pp.cfg.retention",
    "pp.dew.dead_letter",
    "pp.dew.enabled",
    "pp.dew.job_policy",
    "pp.dew.late_policy",
    "pp.dew.max_attempts",
    "pp.dew.quarantine",
    "pp.dew.window",
    "pp.gd.backoff",
    "pp.gd.battery",
    "pp.gd.cpu",
    "pp.gd.hidden_tab",
    "pp.gen.enabled",
    "pp.gen.fetch_mode",
    "pp.gen.name",
    "pp.gen.shareable",
    "pp.gpu.charging_only",
    "pp.gpu.enabled",
    "pp.met.benchmark",
    "pp.met.light",
    "pp.p2p.enabled",
    "pp.p2p.fallback",
    "pp.p2p.peer_scope",
    "pp.p2p.result_relay",
    "pp.p2p.rtt_probe",
    "pp.p2p.task_handoff",
    "pp.sch.bounded_queue",
    "pp.sch.fair_share",
    "pp.sch.priority_high",
    "pp.shape.close_policy",
    "pp.shape.warm_initializer",
    "pp.ver.canary",
    "pp.ver.canary_ban",
    "pp.ver.canary_bar",
    "pp.ver.canary_warn",
    "pp.ver.chain",
    "pp.ver.evidence",
    "pp.ver.hash_check",
    "pp.ver.local_after_download",
    "pp.ver.merkle",
    "pp.ver.mode",
    "pp.ver.reputation_recovery",
    "pp.ver.rerun_percent",
    "pp.ver.signed_envelope",
    "pp.wrk.desktops",
    "pp.wrk.laptops",
    "pp.wrk.max",
    "pp.wrk.min",
    "pp.wrk.owner",
    "pp.wrk.per_tab",
    "pp.wrk.phone_battery",
    "pp.wrk.phone_charging",
    "pp.wrk.phone_stop",
    "pp.wrk.phones"
  ],
  "properties": {
    "pp.gen.enabled": {
      "type": "boolean",
      "description": "Master switch; off stops new session and job leasing in this pool"
    },
    "pp.gen.name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 64,
      "description": "Display name shown in nav, monitors and API responses"
    },
    "pp.gen.fetch_mode": {
      "type": "string",
      "enum": [
        "leader",
        "each-tab"
      ],
      "description": "Leader tab per device or every tab fetches on its own; inefficient use always warns"
    },
    "pp.gen.shareable": {
      "type": "boolean",
      "description": "Lets a developer mark outputs shareable; the admin may disable it for the pool"
    },
    "pp.shape.default": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) tab session lease, or (b) task-group pinning to one tab"
    },
    "pp.shape.session_minutes": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1440,
      "description": "Minutes a leased session stays warm before it is released"
    },
    "pp.shape.tabs_per_session": {
      "type": "integer",
      "minimum": 1,
      "maximum": 64,
      "description": "Tabs leased for one session"
    },
    "pp.shape.hosts_per_session": {
      "type": "integer",
      "minimum": 1,
      "maximum": 64,
      "description": "Distinct registered devices per session; one up to the tab count"
    },
    "pp.shape.warm_initializer": {
      "type": "boolean",
      "description": "Artifact preloaded into the session tab before tasks arrive"
    },
    "pp.shape.initializer_state": {
      "type": "string",
      "enum": [
        "keep",
        "discard"
      ],
      "description": "Default state of the warm initializer between sessions"
    },
    "pp.shape.close_policy": {
      "type": "string",
      "enum": [
        "release",
        "drain"
      ],
      "description": "What happens to the tabs on close, expiry or error"
    },
    "pp.shape.lease_store": {
      "type": "string",
      "enum": [
        "pg",
        "redis+pg"
      ],
      "description": "PG leases, or Redis lease plus PG settle; PG is proposed and the default is not decided"
    },
    "pp.wrk.per_tab": {
      "type": "string",
      "enum": [
        "auto",
        "1",
        "2",
        "3",
        "4"
      ],
      "description": "Compute web worker threads inside each wwHost tab"
    },
    "pp.wrk.min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "description": "Lowest number of compute workers this pool allows per tab"
    },
    "pp.wrk.max": {
      "type": "integer",
      "minimum": 1,
      "maximum": 32,
      "description": "Highest number of compute workers per tab; the server admin may raise it where more cores exist"
    },
    "pp.wrk.phones": {
      "type": "integer",
      "minimum": 1,
      "maximum": 2,
      "description": "Workers per tab on phones"
    },
    "pp.wrk.laptops": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "description": "Workers per tab on laptops"
    },
    "pp.wrk.desktops": {
      "type": "integer",
      "minimum": 1,
      "maximum": 8,
      "description": "Workers per tab on desktops"
    },
    "pp.wrk.owner": {
      "type": "string",
      "enum": [
        "auto",
        "1-32"
      ],
      "description": "Workers per tab on owner nodes"
    },
    "pp.wrk.phone_battery": {
      "type": "integer",
      "minimum": 1,
      "maximum": 2,
      "description": "Workers per tab while the phone runs on battery"
    },
    "pp.wrk.phone_charging": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "description": "Workers per tab while charging with the screen on"
    },
    "pp.wrk.phone_stop": {
      "type": "integer",
      "minimum": 5,
      "maximum": 50,
      "description": "Work stops below this battery level"
    },
    "pp.gd.cpu": {
      "type": "string",
      "enum": [
        "low",
        "medium",
        "full"
      ],
      "description": "Contributor CPU effort profile the server asks for"
    },
    "pp.gd.backoff": {
      "type": "boolean",
      "description": "Drops worker effort only when the device is very very busy"
    },
    "pp.gd.battery": {
      "type": "boolean",
      "description": "Applies the phone battery and heat rules where the device supports them"
    },
    "pp.gd.memory": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4096,
      "description": "Upper memory bound asked of each worker"
    },
    "pp.gd.hidden_tab": {
      "type": "boolean",
      "const": false,
      "description": "Deliberately off: hidden and background tabs must keep working"
    },
    "pp.gpu.enabled": {
      "type": "boolean",
      "description": "Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool"
    },
    "pp.gpu.time_limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 3600,
      "description": "Maximum runtime of one GPU task"
    },
    "pp.gpu.memory_limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 8192,
      "description": "Maximum GPU memory one task may request"
    },
    "pp.gpu.charging_only": {
      "type": "boolean",
      "description": "GPU tasks are eligible only when the device is charging"
    },
    "pp.sch.bounded_queue": {
      "type": "boolean",
      "description": "Caps the admitted queue depth; the queue never grows without limit"
    },
    "pp.sch.rejection": {
      "type": "string",
      "enum": [
        "reject-429",
        "block-timeout",
        "drop-oldest",
        "caller-runs"
      ],
      "description": "What happens when the bounded queue is full"
    },
    "pp.sch.priority_high": {
      "type": "boolean",
      "description": "Lets the admin allow the high priority class in this pool"
    },
    "pp.sch.fair_share": {
      "type": "boolean",
      "description": "Shares pool capacity fairly between developers"
    },
    "pp.sch.small_task": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Tasks below this size raise an efficiency warning on all four surfaces"
    },
    "pp.sch.batch_size": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1000,
      "description": "Default task batch size handed to a device class"
    },
    "pp.ver.hash_check": {
      "type": "boolean",
      "description": "Verifies artifact and input SHA-256 on every load, including IndexedDB cache hits"
    },
    "pp.ver.rerun_percent": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "description": "Percentage of accepted results re-run on another worker; exact hashes only"
    },
    "pp.ver.signed_envelope": {
      "type": "boolean",
      "description": "Server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease"
    },
    "pp.ver.canary": {
      "type": "boolean",
      "description": "Schedules hidden known-answer tasks to test worker trustworthiness"
    },
    "pp.ver.canary_warn": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "description": "Reputation score at or below which the worker is warned"
    },
    "pp.ver.canary_bar": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "description": "Reputation score at or below which the worker is barred from tasks"
    },
    "pp.ver.canary_ban": {
      "type": "integer",
      "minimum": 0,
      "maximum": 100,
      "description": "Reputation score at or below which the worker is banned"
    },
    "pp.ver.reputation_recovery": {
      "type": "string",
      "enum": [
        "manual-review",
        "auto"
      ],
      "description": "A ban is lifted by admin review; there is no automatic recovery"
    },
    "pp.ver.mode": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified"
    },
    "pp.ver.k_of_n": {
      "type": "integer",
      "minimum": 1,
      "maximum": 5,
      "description": "Independent checks that must agree before a result counts as verified"
    },
    "pp.ver.local_after_download": {
      "type": "boolean",
      "description": "Lets developers verify results locally after download"
    },
    "pp.ver.evidence": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) server minimal, security log plus SHA-256 of every accepted result, or (b) everything opt-in"
    },
    "pp.ver.chain": {
      "type": "boolean",
      "description": "Per-job hash chain written by the server; built and tested even while off"
    },
    "pp.ver.merkle": {
      "type": "boolean",
      "description": "Adds periodic Merkle roots over the tips of job chains"
    },
    "pp.ver.chain_authority": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance"
    },
    "pp.p2p.enabled": {
      "type": "boolean",
      "description": "Hash-addressed sharing of server-published artifacts and inputs; results always go to the server"
    },
    "pp.p2p.peer_scope": {
      "type": "string",
      "enum": [
        "same-device",
        "same-phone",
        "same-lan",
        "near"
      ],
      "description": "Which peers may serve artifacts: same device, same phone, same LAN or very close proximity"
    },
    "pp.p2p.task_handoff": {
      "type": "boolean",
      "description": "Historical P2P scope, kept as a setting; not an active peer data flow"
    },
    "pp.p2p.result_relay": {
      "type": "boolean",
      "description": "Historical P2P scope, kept as a setting; results are never relayed over P2P"
    },
    "pp.p2p.rtt_probe": {
      "type": "boolean",
      "description": "Uses round-trip time in peer ordering once the probe exists"
    },
    "pp.p2p.fallback": {
      "type": "boolean",
      "description": "Falls back to server or CDN when no peer can serve the artifact"
    },
    "pp.dew.enabled": {
      "type": "boolean",
      "description": "Offline leases with signed receipts so a tab may submit results later"
    },
    "pp.dew.window": {
      "type": "integer",
      "minimum": 1,
      "maximum": 72,
      "description": "How long a signed dew receipt stays valid"
    },
    "pp.dew.late_policy": {
      "type": "string",
      "enum": [
        "a",
        "b",
        "c"
      ],
      "description": "(a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease"
    },
    "pp.dew.max_attempts": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "description": "Attempts allowed for one task before it fails"
    },
    "pp.dew.job_policy": {
      "type": "string",
      "enum": [
        "partial",
        "fail-fast",
        "threshold"
      ],
      "description": "What the job does when a task cannot complete"
    },
    "pp.dew.quarantine": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "description": "Worker crashes before a poison task is quarantined"
    },
    "pp.dew.dead_letter": {
      "type": "boolean",
      "description": "Keeps late results that arrive after the job ended, with no second credit"
    },
    "pp.met.light": {
      "type": "boolean",
      "description": "Per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated"
    },
    "pp.met.benchmark": {
      "type": "boolean",
      "description": "Heavy harness for owner-started thesis runs beside the MVP"
    },
    "pp.cfg.retention": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Number of config versions kept for this admin page"
    },
    "pp.cfg.reason": {
      "type": "boolean",
      "description": "Each Save records who, when, what changed and why"
    },
    "redis.memory.mode": {
      "type": "string",
      "enum": [
        "fixed",
        "dynamic"
      ],
      "default": "fixed",
      "description": "T91 / R42.8: shared Redis cap mode; always noeviction."
    },
    "redis.memory.fixed_gb": {
      "type": "integer",
      "minimum": 1,
      "maximum": 4,
      "default": 1,
      "description": "Fixed cap in binary GB (GiB)."
    },
    "redis.memory.dynamic_percent": {
      "type": "integer",
      "minimum": 10,
      "maximum": 40,
      "default": 10,
      "description": "Percent of Linux MemAvailable, clamped to 1-4 GiB."
    },
    "redis.memory.interval_min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 60,
      "default": 5,
      "description": "Dynamic recomputation interval in minutes."
    },
    "redis.persistence.mode": {
      "type": "string",
      "enum": [
        "aof_everysec",
        "snapshot"
      ],
      "default": "aof_everysec",
      "description": "AOF every second or existing Redis snapshot save rules only."
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the thread-api-admin document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('thread-api-admin', 'thread-api-admin', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/thread-api-admin/1.0.0",
  "title": "CCGSC thread-api-admin page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'thread-api-admin'. The settings are the ones of the admin manual page thread-api-admin.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^[a-z]{2,3}\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "ta.cfg.reason",
    "ta.cfg.retention",
    "ta.err.cursor",
    "ta.err.idempotency",
    "ta.err.problem_json",
    "ta.err.traceparent",
    "ta.fair.credits",
    "ta.fair.priority_high",
    "ta.fair.rewards",
    "ta.fair.share",
    "ta.keys.approval",
    "ta.keys.issuance",
    "ta.keys.jwt_ttl",
    "ta.keys.oauth2",
    "ta.q.retry_after",
    "ta.queue.bounded",
    "ta.upload.format",
    "ta.upload.hash",
    "ta.upload.malware",
    "ta.upload.review_gate",
    "ta.upload.scan",
    "ta.upload.size",
    "ta.ver.contract_tests",
    "ta.ver.major",
    "ta.ver.notice"
  ],
  "properties": {
    "ta.keys.issuance": {
      "type": "string",
      "enum": [
        "manual",
        "self-service"
      ],
      "description": "Manual issue by the admin, or developer self-service"
    },
    "ta.keys.approval": {
      "type": "string",
      "enum": [
        "approve-each",
        "auto-approve-all"
      ],
      "description": "Approve each key request, or auto-approve all"
    },
    "ta.keys.lifetime": {
      "type": "string",
      "minLength": 1,
      "maxLength": 32,
      "description": "How long an issued key stays usable; the key is shown once and stored hashed"
    },
    "ta.keys.scope": {
      "type": "array",
      "items": {
        "type": "string",
        "enum": [
          "pool",
          "artifacts",
          "quota",
          "events"
        ]
      },
      "minItems": 1,
      "maxItems": 4,
      "description": "Permissions bound to a key, such as pool, artifacts, quota or events"
    },
    "ta.keys.jwt_ttl": {
      "type": "integer",
      "minimum": 1,
      "maximum": 60,
      "description": "Lifetime of the short token exchanged from the key"
    },
    "ta.keys.oauth2": {
      "type": "boolean",
      "description": "Optional token endpoint for future SSO"
    },
    "ta.q.running_jobs": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1000,
      "description": "Jobs that may run at the same time on one key"
    },
    "ta.q.queued_tasks": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100000,
      "description": "Tasks that may wait in the queue on one key"
    },
    "ta.q.task_size": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1048576,
      "description": "Maximum size of one task payload"
    },
    "ta.q.task_time": {
      "type": "integer",
      "minimum": 1,
      "maximum": 86400,
      "description": "Maximum time one task may run"
    },
    "ta.q.req_per_min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100000,
      "description": "Maximum API requests per minute on one key"
    },
    "ta.q.retry_after": {
      "type": "boolean",
      "description": "Sends Retry-After with every rate-limit rejection"
    },
    "ta.fair.share": {
      "type": "boolean",
      "description": "Shares capacity fairly between developers"
    },
    "ta.fair.priority_high": {
      "type": "boolean",
      "description": "Allows the high priority class when the admin permits it"
    },
    "ta.fair.credits": {
      "type": "boolean",
      "description": "Meters CPU-seconds and GPU-seconds per key and reports them"
    },
    "ta.fair.rewards": {
      "type": "boolean",
      "description": "Issues reports and rewards from metered credits; no payment now"
    },
    "ta.queue.bounded": {
      "type": "boolean",
      "description": "Caps the admitted queue depth"
    },
    "ta.queue.rejection": {
      "type": "string",
      "enum": [
        "reject-429",
        "block-timeout",
        "drop-oldest",
        "caller-runs"
      ],
      "description": "What happens when the queue is full"
    },
    "ta.err.problem_json": {
      "type": "boolean",
      "description": "Same error envelope and code list on REST, SSE and gRPC"
    },
    "ta.err.idempotency": {
      "type": "boolean",
      "description": "Same key and body replays; a different body is a conflict"
    },
    "ta.err.traceparent": {
      "type": "boolean",
      "description": "Accepts W3C traceparent on submit and echoes it into the audit chain"
    },
    "ta.err.cursor": {
      "type": "boolean",
      "description": "Opaque cursor paging with limit on list routes"
    },
    "ta.upload.format": {
      "type": "boolean",
      "description": "Checks the uploaded module and data format"
    },
    "ta.upload.size": {
      "type": "boolean",
      "description": "Enforces the maximum upload size"
    },
    "ta.upload.hash": {
      "type": "boolean",
      "description": "Checks uploaded bytes against the declared SHA-256"
    },
    "ta.upload.scan": {
      "type": "boolean",
      "description": "Scans imports and permissions in the uploaded code"
    },
    "ta.upload.review_gate": {
      "type": "boolean",
      "description": "A new code upload waits for admin review"
    },
    "ta.upload.malware": {
      "type": "boolean",
      "description": "Runs the malware and abuse scan on uploads"
    },
    "ta.ver.major": {
      "type": "string",
      "minLength": 1,
      "maxLength": 64,
      "description": "Major version segment of this route family"
    },
    "ta.ver.notice": {
      "type": "integer",
      "minimum": 3,
      "maximum": 12,
      "description": "Notice period before the next major version"
    },
    "ta.ver.contract_tests": {
      "type": "boolean",
      "description": "Contract tests generated from OpenAPI run on every change"
    },
    "ta.cfg.retention": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Number of config versions kept for this admin page"
    },
    "ta.cfg.reason": {
      "type": "boolean",
      "description": "Each Save records who, when, what changed and why"
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;

-- the process-api-admin document
INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current) VALUES
    ('process-api-admin', 'process-api-admin', '1.0.0', $schema$
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ccgsc.digitalbd.org/schemas/pools/process-api-admin/1.0.0",
  "title": "CCGSC process-api-admin page config",
  "description": "JSON Schema 2020-12 for the config jsonb of one pool_config_version row whose page = 'process-api-admin'. The settings are the ones of the admin manual page process-api-admin.html (docs/ADMIN_MANUALS_SOURCE.md). Every Save is validated against this document (WWPP-R40.2); the matching PostgreSQL DDL is pool_config.sql.",
  "type": "object",
  "additionalProperties": false,
  "propertyNames": {
    "pattern": "^[a-z]{2,3}\\.[a-z0-9_]+(\\.[a-z0-9_]+)*$"
  },
  "required": [
    "pa.cfg.reason",
    "pa.cfg.retention",
    "pa.err.cursor",
    "pa.err.idempotency",
    "pa.err.problem_json",
    "pa.err.traceparent",
    "pa.fair.credits",
    "pa.fair.priority_high",
    "pa.fair.rewards",
    "pa.fair.share",
    "pa.keys.approval",
    "pa.keys.issuance",
    "pa.keys.jwt_ttl",
    "pa.keys.oauth2",
    "pa.q.retry_after",
    "pa.queue.bounded",
    "pa.ses.close_policy",
    "pa.ses.enabled",
    "pa.ses.warm_initializer",
    "pa.upload.format",
    "pa.upload.hash",
    "pa.upload.malware",
    "pa.upload.review_gate",
    "pa.upload.scan",
    "pa.upload.size",
    "pa.ver.contract_tests",
    "pa.ver.major",
    "pa.ver.notice"
  ],
  "properties": {
    "pa.keys.issuance": {
      "type": "string",
      "enum": [
        "manual",
        "self-service"
      ],
      "description": "Manual issue by the admin, or developer self-service"
    },
    "pa.keys.approval": {
      "type": "string",
      "enum": [
        "approve-each",
        "auto-approve-all"
      ],
      "description": "Approve each key request, or auto-approve all"
    },
    "pa.keys.lifetime": {
      "type": "string",
      "minLength": 1,
      "maxLength": 32,
      "description": "How long an issued key stays usable; the key is shown once and stored hashed"
    },
    "pa.keys.scope": {
      "type": "array",
      "items": {
        "type": "string",
        "enum": [
          "pool",
          "artifacts",
          "quota",
          "events"
        ]
      },
      "minItems": 1,
      "maxItems": 4,
      "description": "Permissions bound to a key, such as pool, artifacts, quota or events"
    },
    "pa.keys.jwt_ttl": {
      "type": "integer",
      "minimum": 1,
      "maximum": 60,
      "description": "Lifetime of the short token exchanged from the key"
    },
    "pa.keys.oauth2": {
      "type": "boolean",
      "description": "Optional token endpoint for future SSO"
    },
    "pa.q.running_jobs": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1000,
      "description": "Jobs that may run at the same time on one key"
    },
    "pa.q.queued_tasks": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100000,
      "description": "Tasks that may wait in the queue on one key"
    },
    "pa.q.task_size": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1048576,
      "description": "Maximum size of one task payload"
    },
    "pa.q.task_time": {
      "type": "integer",
      "minimum": 1,
      "maximum": 86400,
      "description": "Maximum time one task may run"
    },
    "pa.q.req_per_min": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100000,
      "description": "Maximum API requests per minute on one key"
    },
    "pa.q.retry_after": {
      "type": "boolean",
      "description": "Sends Retry-After with every rate-limit rejection"
    },
    "pa.fair.share": {
      "type": "boolean",
      "description": "Shares capacity fairly between developers"
    },
    "pa.fair.priority_high": {
      "type": "boolean",
      "description": "Allows the high priority class when the admin permits it"
    },
    "pa.fair.credits": {
      "type": "boolean",
      "description": "Meters CPU-seconds and GPU-seconds per key and reports them"
    },
    "pa.fair.rewards": {
      "type": "boolean",
      "description": "Issues reports and rewards from metered credits; no payment now"
    },
    "pa.ses.enabled": {
      "type": "boolean",
      "description": "Enables the explicit process-pool session API beside plain jobs"
    },
    "pa.ses.shape": {
      "type": "string",
      "enum": [
        "a",
        "b"
      ],
      "description": "(a) tab session lease, or (b) task-group pinning"
    },
    "pa.ses.lease_minutes": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1440,
      "description": "Minutes a leased session stays warm before release"
    },
    "pa.ses.tab_count": {
      "type": "integer",
      "minimum": 1,
      "maximum": 64,
      "description": "Tabs leased for one session"
    },
    "pa.ses.host_count": {
      "type": "integer",
      "minimum": 1,
      "maximum": 64,
      "description": "Distinct registered devices per session; one up to the tab count"
    },
    "pa.ses.warm_initializer": {
      "type": "boolean",
      "description": "Artifact preloaded into the session tab before tasks arrive"
    },
    "pa.ses.initializer_state": {
      "type": "string",
      "enum": [
        "keep",
        "discard"
      ],
      "description": "Default state of the warm initializer between sessions"
    },
    "pa.ses.close_policy": {
      "type": "string",
      "enum": [
        "release",
        "drain"
      ],
      "description": "What happens to the tabs on close, expiry or error"
    },
    "pa.queue.bounded": {
      "type": "boolean",
      "description": "Caps the admitted queue depth"
    },
    "pa.queue.rejection": {
      "type": "string",
      "enum": [
        "reject-429",
        "block-timeout",
        "drop-oldest",
        "caller-runs"
      ],
      "description": "What happens when the queue is full"
    },
    "pa.err.problem_json": {
      "type": "boolean",
      "description": "Same error envelope and code list on REST, SSE and gRPC"
    },
    "pa.err.idempotency": {
      "type": "boolean",
      "description": "Same key and body replays; a different body is a conflict"
    },
    "pa.err.traceparent": {
      "type": "boolean",
      "description": "Accepts W3C traceparent on submit and echoes it into the audit chain"
    },
    "pa.err.cursor": {
      "type": "boolean",
      "description": "Opaque cursor paging with limit on list routes"
    },
    "pa.upload.format": {
      "type": "boolean",
      "description": "Checks the uploaded module and data format"
    },
    "pa.upload.size": {
      "type": "boolean",
      "description": "Enforces the maximum upload size"
    },
    "pa.upload.hash": {
      "type": "boolean",
      "description": "Checks uploaded bytes against the declared SHA-256"
    },
    "pa.upload.scan": {
      "type": "boolean",
      "description": "Scans imports and permissions in the uploaded code"
    },
    "pa.upload.review_gate": {
      "type": "boolean",
      "description": "A new code upload waits for admin review"
    },
    "pa.upload.malware": {
      "type": "boolean",
      "description": "Runs the malware and abuse scan on uploads"
    },
    "pa.ver.major": {
      "type": "string",
      "minLength": 1,
      "maxLength": 64,
      "description": "Major version segment of this route family"
    },
    "pa.ver.notice": {
      "type": "integer",
      "minimum": 3,
      "maximum": 12,
      "description": "Notice period before the next major version"
    },
    "pa.ver.contract_tests": {
      "type": "boolean",
      "description": "Contract tests generated from OpenAPI run on every change"
    },
    "pa.cfg.retention": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Number of config versions kept for this admin page"
    },
    "pa.cfg.reason": {
      "type": "boolean",
      "description": "Each Save records who, when, what changed and why"
    }
  },
  "x_source": "docs/ADMIN_MANUALS_SOURCE.md",
  "x_end": "END"
}
$schema$::jsonb, true)
ON CONFLICT (page, schema_version) DO UPDATE
    SET schema_name    = EXCLUDED.schema_name,
        schema         = EXCLUDED.schema,
        is_current     = true;


COMMIT;

-- ============================================================
-- End of pool_config.sql
-- END
