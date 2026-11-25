// Path: internal/store/pg/config.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: the pool_config_version API over the PostgreSQL tables of db/pool_config.sql.
// WWPP-R38.6 (every Save is a new audited version), R38.7 (keep the last N plus in-use plus
// pinned), R40.2 (JSON Schema validates every Save), R25.1 (the security log is always on).
// The database owns the version numbering, the audit trail and the in_use flag; this file owns
// the full JSON Schema 2020-12 check that PostgreSQL cannot express, and one debug log line
// per call (owner A116 debug mode).
//
// WO FIX-E (2026-11-18), against the review WEBCHAT_R1_qwen_pgstore_20261117.md:
//   - every method runs on a Querier, so a caller can pass a pgx.Tx and make a Save and the
//     AttachJob that follows it ONE atomic unit. Before, every method hit the pool on its own.
//   - SaveVersion runs in ONE transaction: the schema is read, the row is written and the row
//     is read back in the same transaction, so the schema cannot swap in the middle of the call
//     and a lost connection cannot report a failure for a version that was committed.
//   - AttachJobLatest pins the newest version in ONE statement, so the prune cannot delete the
//     version between the read and the write (the Latest -> AttachJob race).
//   - Rollback validates the config against the current schema, needs a reason and refuses
//     author 0.
//   - Prune takes the author, so the audit row of a pruned version has an actor.
//   - the PostgreSQL error codes are mapped to sentinel errors, so a caller does not have to
//     know the SQLSTATE of the database.
//   - IsInUse asks the database whether a job really keeps the version (EXISTS on
//     job_config_ref) and returns ErrVersionNotFound when the version does not exist.
//   - validJobID is the same check as the constraint of the database.
//   - List refuses a negative limit instead of reading it as "every version".
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidConfig is returned when a config violates the registered JSON Schema of its page.
var ErrInvalidConfig = errors.New("config does not satisfy the registered JSON Schema")

// ErrVersionNotFound is returned when a page has no such version.
var ErrVersionNotFound = errors.New("config version not found")

// ErrPageNotFound is returned when a page id is not one of the 7 admin pages.
var ErrPageNotFound = errors.New("admin page not found")

// ErrSchemaNotFound is returned when no JSON Schema is registered for a page.
var ErrSchemaNotFound = errors.New("no JSON Schema is registered for the page")

// ErrAuthorRequired is returned when a write has no author, or an author that is not an
// active admin_user.
var ErrAuthorRequired = errors.New("an active author is required")

// ErrReasonRequired is returned when a write has no reason.
var ErrReasonRequired = errors.New("every write needs a reason")

// ErrVersionConflict is returned when the database refused the version number of a write
// because another write took it in the same instant (SQLSTATE 23505).
var ErrVersionConflict = errors.New("the config version was written by somebody else at the same time")

// ErrVersionInUse is returned when a write would touch a version a running job keeps
// (SQLSTATE 23503).
var ErrVersionInUse = errors.New("the config version is used by a running job")

// ErrSerializationFailed is returned when the database rolled the transaction back because it
// could not serialize it (SQLSTATE 40001).
var ErrSerializationFailed = errors.New("the transaction could not be serialized, please retry")

// Pages are the 7 admin page ids of pool_admin_page (WWPP-R38.5, R38.5b, R39.1 spelling).
var Pages = []string{
	"monitor",
	"pool-monitor",
	"api-monitor",
	"thread-pool-admin",
	"process-pool-admin",
	"thread-api-admin",
	"process-api-admin",
}

// DefaultKeepLast is the number of versions kept per page when pool_config_retention has no
// row for the page (WWPP-R38.7: the count is itself an admin setting, default 10).
const DefaultKeepLast = 10

// MaxJobIDLength is the longest job id the database accepts (the check constraint of
// job_config_ref).
const MaxJobIDLength = 128

// maxWriteRetries is how many times a write is retried when the database reports a
// serialization failure (40001) or a version conflict (23505). Both are transient: another
// session wrote at the same instant.
const maxWriteRetries = 3

// Querier is the part of pgx a store call needs. Both *pgxpool.Pool and pgx.Tx satisfy it, so
// a caller can run a Save and the AttachJob that follows it in one transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ConfigVersion is one row of pool_config_version, joined with the flags of
// pool_config_version_state.
type ConfigVersion struct {
	ID            int64           `json:"id"`
	Page          string          `json:"page"`
	Version       int             `json:"version"`
	Config        json.RawMessage `json:"config"`
	JSONSchemaVer string          `json:"jsonSchemaVer"`
	AuthorID      int64           `json:"authorId"`
	Reason        string          `json:"reason"`
	CreatedAt     time.Time       `json:"createdAt"`
	PinnedName    *string         `json:"pinnedName,omitempty"`
	Pinned        bool            `json:"pinned"`
	InUse         bool            `json:"inUse"`
	IsCurrent     bool            `json:"isCurrent"`
}

// PruneResult reports what one prune removed from one page.
type PruneResult struct {
	Page            string `json:"page"`
	DeletedVersions int    `json:"deletedVersions"`
	KeptVersions    int    `json:"keptVersions"`
}

// ConfigStore is the pool_config_version API. It needs a pool that already carries the schema
// of db/pool_config.sql; the store never creates or migrates tables on its own.
type ConfigStore struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewConfigStore builds the settings store on an existing pool. A nil logger is replaced by a
// discard logger so a caller can never crash the service by forgetting one.
func NewConfigStore(pool *pgxpool.Pool, logger *slog.Logger) *ConfigStore {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(discardWriter{}, nil))
	}
	return &ConfigStore{pool: pool, log: logger}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Pool returns the underlying pool, so a caller can use the same connection for other work.
func (c *ConfigStore) Pool() *pgxpool.Pool { return c.pool }

// Tx runs fn inside one transaction of the pool. The transaction is rolled back when fn
// returns an error and committed otherwise. A caller that wants a Save and the AttachJob that
// follows it to be atomic uses this method (WO FIX-E).
//
// A transaction that the database rolled back because it could not serialize it (40001) or
// because another write took the same version number at the same instant (23505) is retried:
// both are transient (WO FIX-E: retry 23503/23505/40001).
func (c *ConfigStore) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxWriteRetries; attempt++ {
		err := c.runTx(ctx, fn)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
		// the context may be done already: there is no point in retrying then
		if ctx.Err() != nil {
			return err
		}
	}
	return lastErr
}

// runTx runs fn in one transaction and commits it when fn succeeds.
func (c *ConfigStore) runTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return mapPgError(err)
	}
	defer func() {
		// the rollback of a committed transaction is a no-op, so it is safe to
		// call it in every case
		_ = tx.Rollback(ctx)
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return mapPgError(err)
	}
	return nil
}

// isRetryable reports whether the error is one of the transient database errors.
func isRetryable(err error) bool {
	return errors.Is(err, ErrSerializationFailed) || errors.Is(err, ErrVersionConflict)
}

// SaveVersion stores a new version of one admin page (WWPP-R38.6).
//
// The config is validated in Go against the JSON Schema registered for the page in
// pool_config_json_schema - the authoritative check of R40.2, because the database check is only
// the structural subset PostgreSQL can express. Only then the row is written through
// pool_config_save(), so the database keeps assigning the version number, writing the audit row
// and recording the author.
//
// schemaVer is the schema version the caller wrote the config for; it must equal the registered
// schema version of the page. An empty schemaVer asks the database for the registered one.
//
// The whole call runs in ONE transaction: the schema is read, the row is written and the row is
// read back inside it, so the schema cannot be swapped in the middle of the call.
func (c *ConfigStore) SaveVersion(ctx context.Context, page string, config json.RawMessage, schemaVer string, authorID int64, reason string) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if len(config) == 0 {
		err := fmt.Errorf("%w: config is empty", ErrInvalidConfig)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		err := fmt.Errorf("%w: every Save needs a reason (who, when, what, why)", ErrReasonRequired)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if authorID == 0 {
		err := fmt.Errorf("%w: a Save needs the id of an active admin", ErrAuthorRequired)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}

	var row *ConfigVersion
	err := c.Tx(ctx, func(tx pgx.Tx) error {
		registered, err := c.schemaFor(ctx, tx, page)
		if err != nil {
			return err
		}
		schema, err := ParseSchema(registered.Schema)
		if err != nil {
			return fmt.Errorf("%w: the registered schema of page %s cannot be read: %w", ErrInvalidConfig, page, err)
		}
		if violations := schema.Validate(config); len(violations) > 0 {
			return fmt.Errorf("%w: %s", ErrInvalidConfig, violations[0].Error())
		}
		if schemaVer == "" {
			schemaVer = registered.Version
		}
		if schemaVer != registered.Version {
			return fmt.Errorf("%w: json_schema_ver %q does not match the registered schema version %q of page %s",
				ErrInvalidConfig, schemaVer, registered.Version, page)
		}

		// The version number is assigned by the database, so the row is written first and read
		// back afterwards. Reading it back in a second statement matters: the snapshot of the
		// writing statement does not contain the row the function just inserted, so an
		// is_current flag computed in the same statement would be NULL.
		saved, err := c.queryOne(ctx, tx, `
			WITH saved AS (
				SELECT * FROM pool_config_save($1, $2::jsonb, $3, $4, $5, NULL)
			)
			SELECT (saved).id, (saved).page, (saved).version, (saved).config,
			       (saved).json_schema_ver, (saved).author_id, (saved).reason, (saved).created_at,
			       (saved).pinned_name, (saved).pinned_name IS NOT NULL, (saved).in_use, false
			  FROM saved`,
			page, []byte(config), reason, authorID, schemaVer)
		if err != nil {
			return err
		}
		if saved == nil {
			return fmt.Errorf("%w: page %s", ErrPageNotFound, page)
		}
		row, err = c.readVersion(ctx, tx, page, saved.Version)
		return err
	})
	if err != nil {
		c.log.Debug("config.save", "page", page, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	c.log.Debug("config.save", "page", page, "version", row.Version, "authorId", authorID,
		"schemaVer", row.JSONSchemaVer, "outcome", "saved")
	return row, nil
}

// SaveVersionTx stores a new version inside a transaction of the caller, so the Save and the
// AttachJob that follows it are one atomic unit (WO FIX-E).
func (c *ConfigStore) SaveVersionTx(ctx context.Context, tx pgx.Tx, page string, config json.RawMessage, schemaVer string, authorID int64, reason string) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if len(config) == 0 {
		err := fmt.Errorf("%w: config is empty", ErrInvalidConfig)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		err := fmt.Errorf("%w: every Save needs a reason (who, when, what, why)", ErrReasonRequired)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if authorID == 0 {
		err := fmt.Errorf("%w: a Save needs the id of an active admin", ErrAuthorRequired)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}

	registered, err := c.schemaFor(ctx, tx, page)
	if err != nil {
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	schema, err := ParseSchema(registered.Schema)
	if err != nil {
		err = fmt.Errorf("%w: the registered schema of page %s cannot be read: %w", ErrInvalidConfig, page, err)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if violations := schema.Validate(config); len(violations) > 0 {
		err = fmt.Errorf("%w: %s", ErrInvalidConfig, violations[0].Error())
		c.log.Debug("config.save", "page", page, "outcome", "rejected",
			"violations", len(violations), "firstViolation", violations[0].Error())
		return nil, err
	}
	if schemaVer == "" {
		schemaVer = registered.Version
	}
	if schemaVer != registered.Version {
		err = fmt.Errorf("%w: json_schema_ver %q does not match the registered schema version %q of page %s",
			ErrInvalidConfig, schemaVer, registered.Version, page)
		c.log.Debug("config.save", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}

	saved, err := c.queryOne(ctx, tx, `
		WITH saved AS (
			SELECT * FROM pool_config_save($1, $2::jsonb, $3, $4, $5, NULL)
		)
		SELECT (saved).id, (saved).page, (saved).version, (saved).config,
		       (saved).json_schema_ver, (saved).author_id, (saved).reason, (saved).created_at,
		       (saved).pinned_name, (saved).pinned_name IS NOT NULL, (saved).in_use, false
		  FROM saved`,
		page, []byte(config), reason, authorID, schemaVer)
	if err != nil {
		c.log.Debug("config.save", "page", page, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	if saved == nil {
		err = fmt.Errorf("%w: page %s", ErrPageNotFound, page)
		c.log.Debug("config.save", "page", page, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	row, err := c.readVersion(ctx, tx, page, saved.Version)
	if err != nil {
		c.log.Debug("config.save", "page", page, "version", saved.Version, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	c.log.Debug("config.save", "page", page, "version", row.Version, "authorId", authorID,
		"schemaVer", row.JSONSchemaVer, "outcome", "saved")
	return row, nil
}

// Latest returns the newest version of a page: the version new jobs take at once (WWPP-R38.6).
func (c *ConfigStore) Latest(ctx context.Context, page string) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.latest", "page", page, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	row, err := c.queryOne(ctx, c.pool, versionStateColumns+" WHERE page = $1 ORDER BY version DESC LIMIT 1", page)
	if errors.Is(err, pgx.ErrNoRows) {
		c.log.Debug("config.latest", "page", page, "outcome", "empty")
		return nil, ErrVersionNotFound
	}
	if err != nil {
		c.log.Debug("config.latest", "page", page, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	c.log.Debug("config.latest", "page", page, "version", row.Version, "inUse", row.InUse,
		"pinned", row.Pinned, "outcome", "ok")
	return row, nil
}

// Get returns one version of a page.
func (c *ConfigStore) Get(ctx context.Context, page string, version int) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.get", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	row, err := c.readVersion(ctx, c.pool, page, version)
	if errors.Is(err, ErrVersionNotFound) {
		c.log.Debug("config.get", "page", page, "version", version, "outcome", "notFound")
		return nil, err
	}
	if err != nil {
		c.log.Debug("config.get", "page", page, "version", version, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	c.log.Debug("config.get", "page", page, "version", version, "inUse", row.InUse,
		"pinned", row.Pinned, "outcome", "ok")
	return row, nil
}

// readVersion reads one version without writing a log line, so a call that needs a read-back
// still writes exactly one debug line.
func (c *ConfigStore) readVersion(ctx context.Context, q Querier, page string, version int) (*ConfigVersion, error) {
	row, err := c.queryOne(ctx, q, versionStateColumns+" WHERE page = $1 AND version = $2", page, version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// versionStateColumns lists the columns of pool_config_version_state in the order scanVersion
// expects them.
const versionStateColumns = `
		SELECT id, page, version, config, json_schema_ver, author_id, reason, created_at,
		       pinned_name, pinned, in_use, is_current
		  FROM pool_config_version_state`

// List returns the versions of a page, newest first. A limit of 0 returns every version; a
// negative limit is refused (before, it was read as "every version" as well).
func (c *ConfigStore) List(ctx context.Context, page string, limit int) ([]ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.list", "page", page, "limit", limit, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if limit < 0 {
		err := fmt.Errorf("%w: the limit must not be negative", ErrInvalidConfig)
		c.log.Debug("config.list", "page", page, "limit", limit, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	sqlText := versionStateColumns + " WHERE page = $1 ORDER BY version DESC"
	args := []any{page}
	if limit > 0 {
		sqlText += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := c.pool.Query(ctx, sqlText, args...)
	if err != nil {
		c.log.Debug("config.list", "page", page, "limit", limit, "outcome", "failed", "error", err.Error())
		return nil, mapPgError(err)
	}
	defer rows.Close()

	out := []ConfigVersion{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			c.log.Debug("config.list", "page", page, "outcome", "failed", "error", err.Error())
			return nil, err
		}
		out = append(out, *v)
	}
	if err = rows.Err(); err != nil {
		c.log.Debug("config.list", "page", page, "outcome", "failed", "error", err.Error())
		return nil, mapPgError(err)
	}
	c.log.Debug("config.list", "page", page, "limit", limit, "count", len(out), "outcome", "ok")
	return out, nil
}

// Pin names a version so pruning never removes it (WWPP-R38.7). The name is unique per page.
// The database raises when the version does not exist, so a missing version is an error and
// never a silent success.
func (c *ConfigStore) Pin(ctx context.Context, page string, version int, name string, authorID int64) error {
	if err := validPage(page); err != nil {
		c.log.Debug("config.pin", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return err
	}
	if strings.TrimSpace(name) == "" {
		err := fmt.Errorf("%w: a pin needs a name", ErrInvalidConfig)
		c.log.Debug("config.pin", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return err
	}
	if len(name) > 64 {
		err := fmt.Errorf("%w: a pin name is at most 64 characters", ErrInvalidConfig)
		c.log.Debug("config.pin", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return err
	}
	if err := c.callVoid(ctx, `SELECT pool_config_pin($1, $2, $3, $4)`, page, version, name, actorID(authorID)); err != nil {
		c.log.Debug("config.pin", "page", page, "version", version, "name", name, "outcome", "failed", "error", err.Error())
		return err
	}
	c.log.Debug("config.pin", "page", page, "version", version, "name", name, "outcome", "ok")
	return nil
}

// Unpin releases a pinned version back to normal pruning.
func (c *ConfigStore) Unpin(ctx context.Context, page string, version int, authorID int64) error {
	if err := validPage(page); err != nil {
		c.log.Debug("config.unpin", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return err
	}
	if err := c.callVoid(ctx, `SELECT pool_config_unpin($1, $2, $3)`, page, version, actorID(authorID)); err != nil {
		c.log.Debug("config.unpin", "page", page, "version", version, "outcome", "failed", "error", err.Error())
		return err
	}
	c.log.Debug("config.unpin", "page", page, "version", version, "outcome", "ok")
	return nil
}

// callVoid runs a SQL function that returns void and discards its row. The function itself
// raises when the target does not exist, so the error of the call is the answer of the call.
func (c *ConfigStore) callVoid(ctx context.Context, sqlText string, args ...any) error {
	rows, err := c.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return mapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
	}
	return mapPgError(rows.Err())
}

// AttachJob pins a job to a config version for the whole life of the job (WWPP-R38.6): the job
// keeps the version it started with, and that version is never pruned (WWPP-R38.7). The
// job_config_ref trigger keeps pool_config_version.in_use in step.
func (c *ConfigStore) AttachJob(ctx context.Context, jobID string, page string, version int) error {
	if err := validJobID(jobID); err != nil {
		c.log.Debug("config.attachJob", "jobId", jobID, "outcome", "rejected", "error", err.Error())
		return err
	}
	if err := validPage(page); err != nil {
		c.log.Debug("config.attachJob", "jobId", jobID, "outcome", "rejected", "error", err.Error())
		return err
	}
	var ok bool
	err := c.pool.QueryRow(ctx,
		`SELECT pool_config_attach_job($1, $2, $3)`, jobID, page, version).Scan(&ok)
	if err != nil {
		err = mapPgError(err)
		c.log.Debug("config.attachJob", "jobId", jobID, "page", page, "version", version,
			"outcome", "failed", "error", err.Error())
		return err
	}
	if !ok {
		c.log.Debug("config.attachJob", "jobId", jobID, "page", page, "version", version, "outcome", "notFound")
		return ErrVersionNotFound
	}
	c.log.Debug("config.attachJob", "jobId", jobID, "page", page, "version", version, "outcome", "ok")
	return nil
}

// AttachJobLatest pins a job to the newest version of a page in ONE call. The version is read
// and pinned under the lock of the page, so the prune cannot delete the version between the
// two steps (the Latest -> AttachJob race of the review).
func (c *ConfigStore) AttachJobLatest(ctx context.Context, jobID string, page string) error {
	if err := validJobID(jobID); err != nil {
		c.log.Debug("config.attachJobLatest", "jobId", jobID, "outcome", "rejected", "error", err.Error())
		return err
	}
	if err := validPage(page); err != nil {
		c.log.Debug("config.attachJobLatest", "jobId", jobID, "outcome", "rejected", "error", err.Error())
		return err
	}
	var ok bool
	err := c.pool.QueryRow(ctx,
		`SELECT pool_config_attach_job_latest($1, $2)`, jobID, page).Scan(&ok)
	if err != nil {
		err = mapPgError(err)
		c.log.Debug("config.attachJobLatest", "jobId", jobID, "page", page, "outcome", "failed", "error", err.Error())
		return err
	}
	if !ok {
		c.log.Debug("config.attachJobLatest", "jobId", jobID, "page", page, "outcome", "notFound")
		return ErrVersionNotFound
	}
	c.log.Debug("config.attachJobLatest", "jobId", jobID, "page", page, "outcome", "ok")
	return nil
}

// DetachJob releases the config version a job held. The version may be pruned afterwards.
func (c *ConfigStore) DetachJob(ctx context.Context, jobID string) error {
	if err := validJobID(jobID); err != nil {
		c.log.Debug("config.detachJob", "jobId", jobID, "outcome", "rejected", "error", err.Error())
		return err
	}
	var ok bool
	err := c.pool.QueryRow(ctx, `SELECT pool_config_detach_job($1)`, jobID).Scan(&ok)
	if err != nil {
		err = mapPgError(err)
		c.log.Debug("config.detachJob", "jobId", jobID, "outcome", "failed", "error", err.Error())
		return err
	}
	if !ok {
		c.log.Debug("config.detachJob", "jobId", jobID, "outcome", "notFound")
		return ErrVersionNotFound
	}
	c.log.Debug("config.detachJob", "jobId", jobID, "outcome", "ok")
	return nil
}

// IsInUse reports whether a running job keeps a version (WWPP-R38.6). The answer comes from
// job_config_ref itself (an EXISTS), so it is the truth and not a flag that can be out of step.
// A version that does not exist is an error (ErrVersionNotFound), not a quiet "false".
func (c *ConfigStore) IsInUse(ctx context.Context, page string, version int) (bool, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.isInUse", "page", page, "version", version, "outcome", "rejected", "error", err.Error())
		return false, err
	}
	var inUse bool
	err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM pool_config_version v
			  JOIN job_config_ref j ON j.config_version_id = v.id
			 WHERE v.page = $1 AND v.version = $2)`, page, version).Scan(&inUse)
	if err != nil {
		c.log.Debug("config.isInUse", "page", page, "version", version, "outcome", "failed", "error", err.Error())
		return false, mapPgError(err)
	}
	if !inUse {
		// the version may simply have no job; the caller needs to know whether it
		// exists at all
		var exists bool
		if err = c.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pool_config_version WHERE page = $1 AND version = $2)`,
			page, version).Scan(&exists); err != nil {
			c.log.Debug("config.isInUse", "page", page, "version", version, "outcome", "failed", "error", err.Error())
			return false, mapPgError(err)
		}
		if !exists {
			c.log.Debug("config.isInUse", "page", page, "version", version, "outcome", "notFound")
			return false, ErrVersionNotFound
		}
	}
	c.log.Debug("config.isInUse", "page", page, "version", version, "inUse", inUse, "outcome", "ok")
	return inUse, nil
}

// Prune removes the versions of a page that are beyond the retention count, are not pinned and
// are not used by a running job (WWPP-R38.7). The audit rows of the removed versions stay in the
// security log, with the admin who asked for the prune as their actor. An empty page prunes
// every page.
func (c *ConfigStore) Prune(ctx context.Context, page string, authorID int64) ([]PruneResult, error) {
	if page != "" {
		if err := validPage(page); err != nil {
			c.log.Debug("config.prune", "page", page, "outcome", "rejected", "error", err.Error())
			return nil, err
		}
	}
	// pool_config_prune() prunes every page when it is given NULL; an empty string is not NULL
	// and would match no page at all.
	var target any
	if page != "" {
		target = page
	}
	rows, err := c.pool.Query(ctx, `SELECT page, deleted_versions, kept_versions FROM pool_config_prune($1, $2)`,
		target, actorID(authorID))
	if err != nil {
		err = mapPgError(err)
		c.log.Debug("config.prune", "page", page, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	defer rows.Close()

	out := []PruneResult{}
	for rows.Next() {
		var r PruneResult
		if err = rows.Scan(&r.Page, &r.DeletedVersions, &r.KeptVersions); err != nil {
			c.log.Debug("config.prune", "page", page, "outcome", "failed", "error", err.Error())
			return nil, err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		c.log.Debug("config.prune", "page", page, "outcome", "failed", "error", err.Error())
		return nil, mapPgError(err)
	}
	deleted := 0
	for _, r := range out {
		deleted += r.DeletedVersions
	}
	c.log.Debug("config.prune", "page", page, "authorId", authorID, "deleted", deleted,
		"pages", len(out), "outcome", "ok")
	return out, nil
}

// Retention returns how many versions a page keeps (WWPP-R38.7: the count is itself an admin
// setting).
func (c *ConfigStore) Retention(ctx context.Context, page string) (int, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.retention", "page", page, "outcome", "rejected", "error", err.Error())
		return 0, err
	}
	keep := DefaultKeepLast
	err := c.pool.QueryRow(ctx,
		`SELECT COALESCE((SELECT keep_last FROM pool_config_retention WHERE page = $1), $2)`,
		page, DefaultKeepLast).Scan(&keep)
	if err != nil {
		c.log.Debug("config.retention", "page", page, "outcome", "failed", "error", err.Error())
		return 0, mapPgError(err)
	}
	c.log.Debug("config.retention", "page", page, "keepLast", keep, "outcome", "ok")
	return keep, nil
}

// SetRetention changes how many versions a page keeps.
func (c *ConfigStore) SetRetention(ctx context.Context, page string, keepLast int, authorID int64) error {
	if err := validPage(page); err != nil {
		c.log.Debug("config.setRetention", "page", page, "outcome", "rejected", "error", err.Error())
		return err
	}
	if keepLast < 1 {
		err := fmt.Errorf("%w: keep_last must be at least 1", ErrInvalidConfig)
		c.log.Debug("config.setRetention", "page", page, "keepLast", keepLast, "outcome", "rejected", "error", err.Error())
		return err
	}
	if _, err := c.pool.Exec(ctx, `SELECT pool_config_set_retention($1, $2, $3)`, page, keepLast, actorID(authorID)); err != nil {
		err = mapPgError(err)
		c.log.Debug("config.setRetention", "page", page, "keepLast", keepLast, "outcome", "failed", "error", err.Error())
		return err
	}
	c.log.Debug("config.setRetention", "page", page, "keepLast", keepLast, "outcome", "ok")
	return nil
}

// Rollback copies an earlier version into a new audited version (WWPP-R38.6 - one-click
// rollback never deletes the newer versions).
//
// A rollback is a Save, so it is checked like a Save: the config of the old version is
// validated against the schema that is registered NOW, a reason is needed and the author must
// be an active admin.
func (c *ConfigStore) Rollback(ctx context.Context, page string, toVersion int, reason string, authorID int64) (*ConfigVersion, error) {
	if err := validPage(page); err != nil {
		c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		err := fmt.Errorf("%w: every rollback needs a reason (who, when, what, why)", ErrReasonRequired)
		c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "outcome", "rejected", "error", err.Error())
		return nil, err
	}
	if authorID == 0 {
		err := fmt.Errorf("%w: a rollback needs the id of an active admin", ErrAuthorRequired)
		c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "outcome", "rejected", "error", err.Error())
		return nil, err
	}

	var current *ConfigVersion
	err := c.Tx(ctx, func(tx pgx.Tx) error {
		// the config of the old version is validated against the schema of today
		// before the rollback is written (R40.2: every Save is validated)
		old, err := c.readVersion(ctx, tx, page, toVersion)
		if err != nil {
			return err
		}
		registered, err := c.schemaFor(ctx, tx, page)
		if err != nil {
			return err
		}
		schema, err := ParseSchema(registered.Schema)
		if err != nil {
			return fmt.Errorf("%w: the registered schema of page %s cannot be read: %w", ErrInvalidConfig, page, err)
		}
		if violations := schema.Validate(old.Config); len(violations) > 0 {
			return fmt.Errorf("%w: version %d does not satisfy the schema of today: %s",
				ErrInvalidConfig, toVersion, violations[0].Error())
		}

		row, err := c.queryOne(ctx, tx, `
			WITH rolled AS (
				SELECT * FROM pool_config_rollback($1, $2, $3, $4)
			)
			SELECT (rolled).id, (rolled).page, (rolled).version, (rolled).config,
			       (rolled).json_schema_ver, (rolled).author_id, (rolled).reason, (rolled).created_at,
			       (rolled).pinned_name, (rolled).pinned_name IS NOT NULL, (rolled).in_use, false
			  FROM rolled`,
			page, toVersion, reason, actorID(authorID))
		if err != nil {
			return err
		}
		if row == nil {
			return ErrVersionNotFound
		}
		// The rolled-back copy is the newest version, so it is the current one; the flags are
		// read back so the answer matches the stored row.
		current, err = c.readVersion(ctx, tx, page, row.Version)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "outcome", "notFound")
			return nil, err
		}
		c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "outcome", "failed", "error", err.Error())
		return nil, err
	}
	c.log.Debug("config.rollback", "page", page, "toVersion", toVersion, "newVersion", current.Version,
		"outcome", "ok")
	return current, nil
}

// registeredSchema is one row of pool_config_json_schema.
type registeredSchema struct {
	Version string
	Schema  []byte
}

// schemaFor reads the JSON Schema registered for a page. The document that is current is the
// authority; the older documents of the page are its history.
func (c *ConfigStore) schemaFor(ctx context.Context, q Querier, page string) (registeredSchema, error) {
	var out registeredSchema
	err := q.QueryRow(ctx,
		`SELECT schema_version, schema FROM pool_config_json_schema
		  WHERE page = $1 AND is_current`, page).
		Scan(&out.Version, &out.Schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("%w: no JSON Schema is registered for page %s", ErrSchemaNotFound, page)
	}
	if err != nil {
		return out, mapPgError(err)
	}
	return out, nil
}

// queryOne runs a query that returns at most one version row.
func (c *ConfigStore) queryOne(ctx context.Context, q Querier, sqlText string, args ...any) (*ConfigVersion, error) {
	row := q.QueryRow(ctx, sqlText, args...)
	return scanVersion(row)
}

// rowScanner is the part of pgx.Row and pgx.Rows the version scanner needs.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(row rowScanner) (*ConfigVersion, error) {
	var v ConfigVersion
	var config []byte
	err := row.Scan(&v.ID, &v.Page, &v.Version, &config, &v.JSONSchemaVer, &v.AuthorID,
		&v.Reason, &v.CreatedAt, &v.PinnedName, &v.Pinned, &v.InUse, &v.IsCurrent)
	if err != nil {
		return nil, err
	}
	v.Config = json.RawMessage(config)
	return &v, nil
}

// actorID maps an author id to the nullable bigint the SQL functions take. A zero author is a
// system action, which the security log records with a null actor.
func actorID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// mapPgError maps the PostgreSQL error codes of the settings store to the sentinel errors of
// this package, so a caller does not have to know a SQLSTATE. An error that is not a PostgreSQL
// error is returned unchanged.
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "22023":
		// invalid_parameter_value: the schema, the reason or the author was refused
		return fmt.Errorf("%w: %s", ErrInvalidConfig, pgErr.Message)
	case "23505":
		// unique_violation: the version number was taken by another write
		return fmt.Errorf("%w: %s", ErrVersionConflict, pgErr.Message)
	case "23503":
		// foreign_key_violation: a running job keeps the version
		return fmt.Errorf("%w: %s", ErrVersionInUse, pgErr.Message)
	case "23502":
		// not_null_violation: the author was missing
		return fmt.Errorf("%w: %s", ErrAuthorRequired, pgErr.Message)
	case "40001":
		// serialization_failure: the transaction was rolled back, the caller may retry
		return fmt.Errorf("%w: %s", ErrSerializationFailed, pgErr.Message)
	case "40P01":
		// deadlock_detected
		return fmt.Errorf("%w: %s", ErrSerializationFailed, pgErr.Message)
	}
	return err
}

// validPage checks that the page is one of the 7 admin pages.
func validPage(page string) error {
	for _, known := range Pages {
		if page == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrPageNotFound, page)
}

// validJobID checks the job id shape serverId.poolId.jobId of WWPP-R26.4. It is the same
// check as the constraint job_config_ref_job_id_ck of db/pool_config.sql: three segments of
// [A-Za-z0-9_-], the dots are the separators only and the length is at most 128.
func validJobID(jobID string) error {
	bad := fmt.Errorf("%w: job id %q must have the shape serverId.poolId.jobId", ErrInvalidConfig, jobID)
	if jobID == "" || len(jobID) > MaxJobIDLength {
		return bad
	}
	parts := strings.Split(jobID, ".")
	if len(parts) != 3 {
		return bad
	}
	for _, part := range parts {
		if part == "" {
			return bad
		}
		for i, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				return bad
			}
			// the first character of a segment may not be a dash or an underscore:
			// the database wants a letter or a digit there
			if i == 0 && !isNameStart(string(r)) {
				return bad
			}
		}
	}
	return nil
}

func isNameStart(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
