// Path: internal/store/pg/config_test.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: the pool_config_version API against a real PostgreSQL (WWPP-R38.6, R38.7, R40.2).
// The tests run only when POOLS_PG_DSN is set; without it every test skips, so the package still
// builds and passes on a machine with no database. The DSN database must already carry the
// schema of db/pool_config.sql.
//
// WO FIX-E (2026-11-18):
//   - the concurrency tests are there: two saves at once, a prune against an attach, and two
//     detaches at once (the exact races the review asked for).
//   - every call logs exactly one debug line, and the test drives every method and every
//     outcome; an empty buffer is a failure, not a pass.
//   - the cleanup filters the audit rows by page, actor and version, so a leftover of an
//     earlier run cannot make a test pass.
//   - the test that prunes every page runs last and only touches its own page.
//   - the test that greps the source of the database functions is gone: it proved nothing about
//     the behaviour, so the behaviour itself is proved now.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dsn returns the test DSN, or skips the test when it is not set. When CI is set the DSN must
// be there: a green build with zero coverage is not a pass.
func dsn(t *testing.T) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if v == "" {
		if isCI() {
			t.Fatal("POOLS_PG_DSN is not set and CI is set: the database tests must run")
		}
		t.Skip("POOLS_PG_DSN is not set; skipping the PostgreSQL test")
	}
	return v
}

// isCI reports whether the build runs on a CI machine.
func isCI() bool {
	for _, name := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" && v != "false" && v != "0" {
			return true
		}
	}
	return false
}

// newStore opens the pool and the settings store for one test.
func newStore(t *testing.T) (*ConfigStore, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn(t))
	if err != nil {
		t.Fatalf("cannot connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatalf("cannot ping: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewConfigStore(pool, logger), pool
}

// adminUser creates an active author and returns its id. The username must satisfy the
// admin_user_username_ck check of the DDL: lower-case letters, digits, dot, underscore, dash.
// The name is derived from the test name, so a re-run reuses the same row instead of failing on
// the admin_user_username_lower_uq unique index.
func adminUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	username := "wo-t62-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	if len(username) > 64 {
		username = username[:64]
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO admin_user (username, display_name, roles, password_hash)
		VALUES ($1, $1, ARRAY['admin']::text[], 'test-hash')
		ON CONFLICT (lower(username)) DO UPDATE SET is_active = true
		RETURNING id`, username).Scan(&id)
	if err != nil {
		t.Fatalf("cannot create the author: %v", err)
	}
	return id
}

// adminUserWithSuffix creates a second active author whose name carries a suffix, so a test can
// use two different admins.
func adminUserWithSuffix(t *testing.T, pool *pgxpool.Pool, suffix string) int64 {
	t.Helper()
	username := "wo-t62-" + suffix + "-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	if len(username) > 64 {
		username = username[:64]
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO admin_user (username, display_name, roles, password_hash)
		VALUES ($1, $1, ARRAY['admin']::text[], 'test-hash')
		ON CONFLICT (lower(username)) DO UPDATE SET is_active = true
		RETURNING id`, username).Scan(&id)
	if err != nil {
		t.Fatalf("cannot create the author: %v", err)
	}
	return id
}

// inactiveUser creates an account that is not active and returns its id.
func inactiveUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	username := "wo-t62-off-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	if len(username) > 64 {
		username = username[:64]
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO admin_user (username, display_name, roles, password_hash, is_active)
		VALUES ($1, $1, ARRAY['admin']::text[], 'test-hash', false)
		ON CONFLICT (lower(username)) DO UPDATE SET is_active = false
		RETURNING id`, username).Scan(&id)
	if err != nil {
		t.Fatalf("cannot create the inactive author: %v", err)
	}
	return id
}

// cleanPage removes every trace of one page so each test starts from nothing: the versions,
// the job references and the retention setting (which is itself an admin setting and would
// otherwise leak into the next test). The audit rows of the page are counted before the
// cleanup, so a test can measure its own delta and not the leftovers of an earlier run.
func cleanPage(t *testing.T, pool *pgxpool.Pool, page string) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		DELETE FROM job_config_ref WHERE config_version_id IN
			(SELECT id FROM pool_config_version WHERE page = $1)`, page)
	if err != nil {
		t.Fatalf("cannot clear job refs: %v", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM pool_config_version WHERE page = $1`, page); err != nil {
		t.Fatalf("cannot clear versions: %v", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM pool_config_retention WHERE page = $1`, page); err != nil {
		t.Fatalf("cannot clear the retention: %v", err)
	}
}

// auditCount returns the number of audit rows of one page and one action. The tests use the
// difference of two counts, so a leftover of an earlier run cannot make a test pass.
func auditCount(t *testing.T, pool *pgxpool.Pool, page string, action string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM security_audit_log WHERE page = $1 AND action = $2`,
		page, action).Scan(&n)
	if err != nil {
		t.Fatalf("cannot count the audit rows: %v", err)
	}
	return n
}

// maxAuditID returns the id of the newest row of the security log, or 0 when the log is empty.
// A test uses it as a mark, so the rows it writes itself can be told from the rows of the
// earlier runs (the security log is append-only and is never cleaned).
func maxAuditID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(max(id), 0) FROM security_audit_log`).Scan(&id)
	if err != nil {
		t.Fatalf("cannot read the newest audit row: %v", err)
	}
	return id
}

// threadPage is the only page with a registered JSON Schema in the DDL.
const threadPage = "thread-pool-admin"

// monitorPage is one of the 6 pages that had no registered JSON Schema before WO FIX-E.
const monitorPage = "monitor"

// validThreadConfig is a config that satisfies the registered schema of thread-pool-admin.
var validThreadConfig = json.RawMessage(`{
  "tp.wrk.per_tab": "auto",
  "tp.wrk.min": 1,
  "tp.wrk.max": 4,
  "tp.ver.hash_check": true,
  "tp.ver.rerun_percent": 1,
  "tp.ver.signed_envelope": true,
  "tp.ver.canary": true,
  "tp.p2p.enabled": true,
  "tp.dew.enabled": true,
  "tp.dew.window_seconds": 86400,
  "tp.met.light": true,
  "cfg.keep_last": 10
}`)

// validMonitorConfig is a config that satisfies the registered schema of the monitor page.
var validMonitorConfig = json.RawMessage(`{
  "mon.trust.window_hours": 24,
  "mon.alert.level": "warn",
  "mon.alert.refresh": 10,
  "mon.alert.auto": true,
  "mon.link.numbers": true,
  "mon.link.target": "same-tab",
  "mon.p95.window": "5m",
  "mon.metrics.read": true
}`)

func TestSaveVersionCreatesAuditedVersions(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)
	before := auditCount(t, pool, threadPage, "config_version_saved")

	first, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version is %d, want 1", first.Version)
	}
	// Issue 89 (2026-11-22): the registered schema version is whatever row is current in
	// pool_config_json_schema: "1.0.0" from db/pool_config.sql, or "1.0.0-F5" once the
	// optional db/policy_F5.sql has run. Compare with the current row, not a literal.
	var registered string
	if err := pool.QueryRow(ctx, `SELECT schema_version FROM pool_config_json_schema WHERE page=$1 AND is_current`, threadPage).Scan(&registered); err != nil {
		t.Fatalf("read the registered schema version: %v", err)
	}
	if first.JSONSchemaVer != registered {
		t.Fatalf("schema version %q, want the registered %q", first.JSONSchemaVer, registered)
	}
	if first.InUse || first.Pinned || !first.IsCurrent {
		t.Fatalf("wrong flags on a fresh version: %+v", first)
	}
	if first.Reason != "first save" {
		t.Fatalf("reason %q, want %q", first.Reason, "first save")
	}

	second, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "second save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version is %d, want 2", second.Version)
	}

	// The security log must hold one row per Save (WWPP-R38.6, R25.1). The delta is measured,
	// so the leftovers of an earlier run do not count.
	saved := auditCount(t, pool, threadPage, "config_version_saved") - before
	if saved != 2 {
		t.Fatalf("the security log holds %d new save rows, want exactly 2", saved)
	}
}

// TestEveryPageHasARegisteredSchema is the regression test of the BLOCKER of the review: before,
// only thread-pool-admin had a registered JSON Schema, so the other 6 pages could never be
// saved (the app refuses a Save whose page has no schema).
func TestEveryPageHasARegisteredSchema(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	author := adminUser(t, pool)

	if len(validPageConfigs) != len(Pages) {
		t.Fatalf("there are %d test configs for %d pages", len(validPageConfigs), len(Pages))
	}
	for _, pc := range validPageConfigs {
		t.Run(pc.page, func(t *testing.T) {
			cleanPage(t, pool, pc.page)
			saved, err := store.SaveVersion(ctx, pc.page, pc.config, "", author, "every page can be saved")
			if err != nil {
				t.Fatalf("the page %s cannot be saved: %v", pc.page, err)
			}
			if saved.Version != 1 {
				t.Fatalf("the first version of %s is %d, want 1", pc.page, saved.Version)
			}
		})
	}
}

func TestSaveVersionRejectsConfigBreakingTheSchema(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	// A config that misses a required property must never reach the database.
	bad := json.RawMessage(`{"tp.wrk.min":1}`)
	_, err := store.SaveVersion(ctx, threadPage, bad, "", author, "bad save")
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error %v, want ErrInvalidConfig", err)
	}
	var count int
	if err = pool.QueryRow(ctx,
		`SELECT count(*) FROM pool_config_version WHERE page = $1`, threadPage).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d versions were written for a rejected config", count)
	}
}

func TestSaveVersionRejectsEmptyReasonAndWrongSchemaVersion(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "   "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("empty reason accepted: %v", err)
	}
	if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "9.9.9", author, "wrong ver"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("wrong schema version accepted: %v", err)
	}
	// an author of 0 is refused before the database is asked
	if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", 0, "no author"); !errors.Is(err, ErrAuthorRequired) {
		t.Fatalf("author 0 accepted: %v", err)
	}
}

// TestSaveVersionRefusesAnInactiveAuthor proves that an account that is not active cannot
// author a version (the database refuses it too, the app must not write the row at all).
func TestSaveVersionRefusesAnInactiveAuthor(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	inactive := inactiveUser(t, pool)

	if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", inactive, "inactive author"); err == nil {
		t.Fatal("an inactive author was accepted")
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pool_config_version WHERE page = $1`, threadPage).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d versions were written for an inactive author", count)
	}
}

func TestSaveVersionRejectsUnknownPage(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.SaveVersion(context.Background(), "no-such-page", validThreadConfig, "", 1, "x")
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("unknown page accepted: %v", err)
	}
}

func TestLatestGetAndList(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	var versions []int
	for i := 0; i < 3; i++ {
		v, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		versions = append(versions, v.Version)
	}

	latest, err := store.Latest(ctx, threadPage)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Version != versions[len(versions)-1] {
		t.Fatalf("latest is version %d, want %d", latest.Version, versions[len(versions)-1])
	}
	if !latest.IsCurrent {
		t.Fatal("the latest version is not flagged is_current")
	}

	got, err := store.Get(ctx, threadPage, versions[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != versions[0] {
		t.Fatalf("get returned version %d, want %d", got.Version, versions[0])
	}
	var cfg map[string]any
	if err = json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatalf("the stored config is not JSON: %v", err)
	}
	if cfg["tp.wrk.per_tab"] != "auto" {
		t.Fatalf("the stored config lost its content: %v", cfg)
	}
	if _, err = store.Get(ctx, threadPage, 9999); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("missing version returned %v, want ErrVersionNotFound", err)
	}

	all, err := store.List(ctx, threadPage, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list returned %d versions, want 3", len(all))
	}
	if all[0].Version <= all[1].Version {
		t.Fatal("list is not newest first")
	}
	limited, err := store.List(ctx, threadPage, 2)
	if err != nil {
		t.Fatalf("list with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited list returned %d versions, want 2", len(limited))
	}
	// a negative limit is refused (before, it was read as "every version")
	if _, err = store.List(ctx, threadPage, -1); err == nil {
		t.Fatal("a negative limit was accepted")
	}
}

func TestPinAndUnpin(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err = store.Pin(ctx, threadPage, saved.Version, "thesis-run", author); err != nil {
		t.Fatalf("pin: %v", err)
	}
	pinned, err := store.Get(ctx, threadPage, saved.Version)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !pinned.Pinned || pinned.PinnedName == nil || *pinned.PinnedName != "thesis-run" {
		t.Fatalf("the pin is missing: %+v", pinned)
	}
	if err = store.Pin(ctx, threadPage, saved.Version, "  ", author); err == nil {
		t.Fatal("an empty pin name was accepted")
	}
	// The database raises for a version that does not exist, so the call fails instead of
	// silently doing nothing.
	if err = store.Pin(ctx, threadPage, 9999, "ghost", author); err == nil {
		t.Fatal("a pin on a missing version was accepted")
	}
	// a version that is already pinned under another name is refused
	if err = store.Pin(ctx, threadPage, saved.Version, "another-name", author); err == nil {
		t.Fatal("a second pin name on the same version was accepted")
	}
	// a pin name longer than 64 characters is refused
	if err = store.Pin(ctx, threadPage, saved.Version, strings.Repeat("x", 65), author); err == nil {
		t.Fatal("a pin name of 65 characters was accepted")
	}
	if err = store.Unpin(ctx, threadPage, saved.Version, author); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	unpinned, err := store.Get(ctx, threadPage, saved.Version)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if unpinned.Pinned {
		t.Fatal("the version is still pinned after unpin")
	}
	if err = store.Unpin(ctx, threadPage, 9999, author); err == nil {
		t.Fatal("an unpin on a missing version was accepted")
	}
	// an unpin of a version that is not pinned is refused (the log must not claim it happened)
	if err = store.Unpin(ctx, threadPage, saved.Version, author); err == nil {
		t.Fatal("an unpin of an unpinned version was accepted")
	}
}

func TestInUseFollowsJobAttachment(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if inUse, err := store.IsInUse(ctx, threadPage, saved.Version); err != nil || inUse {
		t.Fatalf("a fresh version is in use: %v %v", inUse, err)
	}

	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err = store.AttachJob(ctx, jobID, threadPage, saved.Version); err != nil {
		t.Fatalf("attach: %v", err)
	}
	inUse, err := store.IsInUse(ctx, threadPage, saved.Version)
	if err != nil || !inUse {
		t.Fatalf("the version is not in use after attach: %v %v", inUse, err)
	}
	row, err := store.Get(ctx, threadPage, saved.Version)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.InUse {
		t.Fatal("the version row does not carry the in_use flag")
	}

	if err = store.DetachJob(ctx, jobID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	inUse, err = store.IsInUse(ctx, threadPage, saved.Version)
	if err != nil || inUse {
		t.Fatalf("the version is still in use after detach: %v %v", inUse, err)
	}
	if err = store.DetachJob(ctx, jobID); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("detaching twice returned %v, want ErrVersionNotFound", err)
	}
	if err = store.AttachJob(ctx, "bad-id", threadPage, saved.Version); err == nil {
		t.Fatal("a malformed job id was accepted")
	}
}

// TestIsInUseReportsAMissingVersion proves that a version that does not exist is an error and
// not a quiet "false" (the review: IsInUse hid the missing version behind COALESCE).
func TestIsInUseReportsAMissingVersion(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.IsInUse(context.Background(), threadPage, 424242)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("error %v, want ErrVersionNotFound", err)
	}
}

func TestPruneKeepsLastTenPlusInUsePlusPinned(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	// 13 versions: the last 10 are v4..v13. Version 1 is pinned and version 2 is used by a
	// running job, so only version 3 is outside the retention and free to remove.
	var saved []*ConfigVersion
	for i := 0; i < 13; i++ {
		v, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		saved = append(saved, v)
	}
	// Version 1 is pinned, version 2 is used by a running job: both must survive.
	if err := store.Pin(ctx, threadPage, saved[0].Version, "keep-me", author); err != nil {
		t.Fatalf("pin: %v", err)
	}
	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err := store.AttachJob(ctx, jobID, threadPage, saved[1].Version); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// the id of the newest audit row, so the rows of THIS prune can be told from the rows of
	// the earlier runs (the security log is append-only)
	pruneAuditBefore := maxAuditID(t, pool)

	results, err := store.Prune(ctx, threadPage, author)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	var deleted, kept int
	for _, r := range results {
		if r.Page == threadPage {
			deleted = r.DeletedVersions
			kept = r.KeptVersions
		}
	}
	if deleted != 1 {
		t.Fatalf("prune removed %d versions, want 1 (only the free old one)", deleted)
	}
	if kept != 12 {
		t.Fatalf("prune kept %d versions, want 12 (the last 10 plus the pinned and the in-use one)", kept)
	}

	if _, err = store.Get(ctx, threadPage, saved[0].Version); err != nil {
		t.Fatalf("the pinned version was pruned: %v", err)
	}
	if _, err = store.Get(ctx, threadPage, saved[1].Version); err != nil {
		t.Fatalf("the in-use version was pruned: %v", err)
	}
	if _, err = store.Get(ctx, threadPage, saved[2].Version); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("the free old version survived the prune: %v", err)
	}
	for i := 3; i < 13; i++ {
		if _, err = store.Get(ctx, threadPage, saved[i].Version); err != nil {
			t.Fatalf("a version inside the last 10 was pruned: %v", err)
		}
	}

	// The audit row of a pruned version stays in the security log (WWPP-R38.7) and carries the
	// admin who asked for the prune (the review: every pruned version had a null actor).
	// The security log is append-only, so the rows of the earlier runs are still there: the
	// count of the rows of THIS prune is the difference of the two counts.
	var actors int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM security_audit_log
		 WHERE page = $1 AND action = 'config_version_pruned' AND actor_id = $2
		   AND id > $3`,
		threadPage, author, pruneAuditBefore).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if actors != 1 {
		t.Fatalf("%d audit rows of the prune carry the actor, want 1", actors)
	}
}

// TestPruneKeepsTheFreeSlotsForTheNewestFreeVersions proves the rule of R38.7 as the review read
// it: the retention counts the newest FREE versions, and a pinned version and a version a
// running job uses are kept IN ADDITION. With keep_last 2, a pinned version and an in-use
// version among the newest ones do not eat a free slot.
func TestPruneKeepsTheFreeSlotsForTheNewestFreeVersions(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	if err := store.SetRetention(ctx, threadPage, 2, author); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	var saved []*ConfigVersion
	for i := 0; i < 5; i++ {
		v, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		saved = append(saved, v)
	}
	// the newest version is pinned and the one before it is used by a running job
	if err := store.Pin(ctx, threadPage, saved[4].Version, "pin-newest", author); err != nil {
		t.Fatalf("pin: %v", err)
	}
	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err := store.AttachJob(ctx, jobID, threadPage, saved[3].Version); err != nil {
		t.Fatalf("attach: %v", err)
	}

	results, err := store.Prune(ctx, threadPage, author)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, r := range results {
		if r.Page != threadPage {
			continue
		}
		// the 2 newest free versions (v1 and v2) plus the pinned v5 and the in-use v4
		if r.KeptVersions != 4 {
			t.Fatalf("prune kept %d versions, want 4 (the 2 newest free ones plus the pin and the in-use one)", r.KeptVersions)
		}
		if r.DeletedVersions != 1 {
			t.Fatalf("prune removed %d versions, want 1 (the oldest free one)", r.DeletedVersions)
		}
	}
	for _, i := range []int{1, 3, 4} {
		if _, err = store.Get(ctx, threadPage, saved[i].Version); err != nil {
			t.Fatalf("version %d was pruned but had to survive: %v", saved[i].Version, err)
		}
	}
	if _, err = store.Get(ctx, threadPage, saved[0].Version); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("the oldest free version survived the prune: %v", err)
	}
}

func TestPruneRespectsTheRetentionSetting(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	keep, err := store.Retention(ctx, threadPage)
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if keep != DefaultKeepLast {
		t.Fatalf("the default retention is %d, want %d", keep, DefaultKeepLast)
	}
	if err = store.SetRetention(ctx, threadPage, 2, author); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err = store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	results, err := store.Prune(ctx, threadPage, author)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, r := range results {
		if r.Page == threadPage && r.KeptVersions != 2 {
			t.Fatalf("prune kept %d versions, want 2", r.KeptVersions)
		}
	}
	if err = store.SetRetention(ctx, threadPage, 0, author); err == nil {
		t.Fatal("keep_last 0 was accepted")
	}
}

func TestRollbackCreatesANewAuditedVersion(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	first, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	changed := json.RawMessage(`{
      "tp.wrk.per_tab": 2,
      "tp.wrk.min": 1,
      "tp.wrk.max": 4,
      "tp.ver.hash_check": true,
      "tp.ver.rerun_percent": 1,
      "tp.ver.signed_envelope": true,
      "tp.ver.canary": true,
      "tp.p2p.enabled": true,
      "tp.dew.enabled": true,
      "tp.dew.window_seconds": 86400,
      "tp.met.light": true,
      "cfg.keep_last": 10
    }`)
	if _, err = store.SaveVersion(ctx, threadPage, changed, "", author, "changed"); err != nil {
		t.Fatalf("save: %v", err)
	}
	rolled, err := store.Rollback(ctx, threadPage, first.Version, "back to the first", author)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolled.Version <= 2 {
		t.Fatalf("the rollback created version %d, want a new one above 2", rolled.Version)
	}
	var cfg map[string]any
	if err = json.Unmarshal(rolled.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["tp.wrk.per_tab"] != "auto" {
		t.Fatalf("the rollback did not copy the old config: %v", cfg)
	}
	// The newer version stays: a rollback never deletes history (WWPP-R38.6).
	if _, err = store.Get(ctx, threadPage, 2); err != nil {
		t.Fatalf("the rollback deleted the newer version: %v", err)
	}
}

// TestRollbackNeedsAReasonAndAnAuthor proves the two checks the review asked for: the reason is
// not empty and the author is not 0. The prefix "rollback to version N: " must not smuggle an
// empty reason through.
func TestRollbackNeedsAReasonAndAnAuthor(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err = store.Rollback(ctx, threadPage, saved.Version, "   ", author); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("an empty reason was accepted: %v", err)
	}
	if _, err = store.Rollback(ctx, threadPage, saved.Version, "a reason", 0); !errors.Is(err, ErrAuthorRequired) {
		t.Fatalf("author 0 was accepted: %v", err)
	}
}

// TestRollbackWorksAfterASchemaBump is the regression test of the BLOCKER of the review: before,
// the rollback copied the old json_schema_ver and the guard demanded the registered one, so
// every rollback raised 22023 after a schema bump. The rollback must revalidate the old config
// against the CURRENT schema and stamp the current version.
func TestRollbackWorksAfterASchemaBump(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	var registered string
	if err = pool.QueryRow(ctx,
		`SELECT schema_version FROM pool_config_json_schema WHERE page = $1 AND is_current`,
		threadPage).Scan(&registered); err != nil {
		t.Fatal(err)
	}
	// the admin registers a newer schema version (the history of the documents is kept)
	if _, err = pool.Exec(ctx, `
		INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current)
		SELECT page, schema_name, '9.9.9', schema, true FROM pool_config_json_schema
		 WHERE page = $1 AND is_current`, threadPage); err != nil {
		t.Fatalf("cannot register the new schema version: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM pool_config_json_schema WHERE page = $1 AND schema_version = '9.9.9'`,
			threadPage); err != nil {
			t.Errorf("cannot remove the test schema version: %v", err)
		}
		if _, err := pool.Exec(context.Background(),
			`UPDATE pool_config_json_schema SET is_current = true
			  WHERE page = $1 AND schema_version = $2`, threadPage, registered); err != nil {
			t.Errorf("cannot restore the schema version: %v", err)
		}
	})

	rolled, err := store.Rollback(ctx, threadPage, saved.Version, "back after the bump", author)
	if err != nil {
		t.Fatalf("the rollback failed after the schema bump: %v", err)
	}
	if rolled.JSONSchemaVer != "9.9.9" {
		t.Fatalf("the rollback stamped the schema version %q, want the current 9.9.9", rolled.JSONSchemaVer)
	}
}

func TestPruneWithAnEmptyPagePrunesEveryPage(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	for i := 0; i < 12; i++ {
		if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	// An empty page means every page. The NULL must reach pool_config_prune(): an empty string
	// would match no page at all and prune nothing.
	results, err := store.Prune(ctx, "", author)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("pruning every page returned no page at all")
	}
	var deleted int
	for _, r := range results {
		deleted += r.DeletedVersions
	}
	if deleted == 0 {
		t.Fatal("pruning every page removed nothing")
	}
}

// TestTheSecurityLogIsAppendOnly proves that the log cannot be changed: no UPDATE, no DELETE
// and no TRUNCATE (the review: TRUNCATE was not covered and the owner could wipe the log).
func TestTheSecurityLogIsAppendOnly(t *testing.T) {
	_, pool := newStore(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE security_audit_log SET action = 'tampered'`); err == nil {
		t.Fatal("an UPDATE of the security log was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM security_audit_log`); err == nil {
		t.Fatal("a DELETE of the security log was accepted")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE security_audit_log`); err == nil {
		t.Fatal("a TRUNCATE of the security log was accepted")
	}
}

// TestAVersionIsImmutable proves that the history cannot be rewritten: the config, the reason,
// the author and the schema version of a version never change.
func TestAVersionIsImmutable(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err = pool.Exec(ctx,
		`UPDATE pool_config_version SET reason = 'rewritten' WHERE id = $1`, saved.ID); err == nil {
		t.Fatal("the reason of a version was changed")
	}
	if _, err = pool.Exec(ctx,
		`UPDATE pool_config_version SET config = '{}'::jsonb WHERE id = $1`, saved.ID); err == nil {
		t.Fatal("the config of a version was changed")
	}
	if _, err = pool.Exec(ctx,
		`UPDATE pool_config_version SET author_id = author_id + 1 WHERE id = $1`, saved.ID); err == nil {
		t.Fatal("the author of a version was changed")
	}
}

// TestTheAuditActorIsTheAuthor proves that the actor of the security log is the author of the
// row and not a session variable: a session that sets app.actor_id to another admin must not be
// able to write that admin into the log.
func TestTheAuditActorIsTheAuthor(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)
	other := adminUserWithSuffix(t, pool, "other")

	// The session that writes claims to be somebody else. The variable is set on EVERY
	// connection of the pool, so the claim really reaches the session of the Save.
	if _, err := pool.Exec(ctx, `SELECT set_config('app.actor_id', $1, false)`, fmt.Sprint(other)); err != nil {
		t.Fatalf("cannot set the session variable: %v", err)
	}
	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "the real author")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// The security log is append-only, so the rows of the earlier runs are still there.
	// The NEWEST row of this page and this version is the one of this Save.
	var actor int64
	if err = pool.QueryRow(ctx, `
		SELECT actor_id FROM security_audit_log
		 WHERE page = $1 AND action = 'config_version_saved' AND to_version = $2
		 ORDER BY id DESC LIMIT 1`,
		threadPage, saved.Version).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != author {
		t.Fatalf("the audit row names the actor %d, want the author %d", actor, author)
	}
}

// TestANonNumericActorVariableDoesNotKillASave proves the second half of the same finding: a
// session that sets app.actor_id to a word must not raise 22P02 on every save.
func TestANonNumericActorVariableDoesNotKillASave(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	if _, err := pool.Exec(ctx, `SELECT set_config('app.actor_id', 'not-a-number', false)`); err != nil {
		t.Fatalf("cannot set the session variable: %v", err)
	}
	if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "still works"); err != nil {
		t.Fatalf("the save failed because of the session variable: %v", err)
	}
}

func TestOpenPoolAndConfigStore(t *testing.T) {
	ctx := context.Background()

	// An empty DSN is refused without touching the network.
	if _, err := OpenPool(ctx, ""); err == nil {
		t.Fatal("an empty DSN was accepted")
	}
	if _, _, err := OpenConfigStore(ctx, "", slog.New(slog.NewJSONHandler(io.Discard, nil))); err == nil {
		t.Fatal("an empty DSN was accepted")
	}

	pool, err := OpenPool(ctx, dsn(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// A nil logger must not crash the store: it falls back to a discard logger.
	store := NewConfigStore(pool, nil)
	if store == nil {
		t.Fatal("the store is nil")
	}
	_, err = store.Latest(ctx, threadPage)
	if err != nil && !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("latest with a nil logger: %v", err)
	}
}

// TestSaveVersionAcceptsTheAutoWorkersValue is the regression test of the PostgreSQL 18 failure
// of WO T6.2: the workers-per-tab setting may be the word "auto" instead of a number. The old
// database check cast the value to numeric, so every Save carrying "auto" failed with
// "invalid input syntax for type numeric: \"auto\"" (SQLSTATE 22P02) and 8 integration tests
// failed on PostgreSQL 18. The Save must go through and keep its content.
func TestSaveVersionAcceptsTheAutoWorkersValue(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "auto save")
	if err != nil {
		t.Fatalf("save with tp.wrk.per_tab=auto: %v", err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(saved.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["tp.wrk.per_tab"] != "auto" {
		t.Fatalf("the stored config lost the 'auto' value: %v", cfg)
	}

	// The same value must also survive the database check of the other "auto" property of the
	// registered schema (tp.wrk.owner).
	ownerConfig := json.RawMessage(`{
  "tp.wrk.per_tab": "auto",
  "tp.wrk.owner": "auto",
  "tp.wrk.min": 1,
  "tp.wrk.max": 4,
  "tp.ver.hash_check": true,
  "tp.ver.rerun_percent": 1,
  "tp.ver.signed_envelope": true,
  "tp.ver.canary": true,
  "tp.p2p.enabled": true,
  "tp.dew.enabled": true,
  "tp.dew.window_seconds": 86400,
  "tp.met.light": true,
  "cfg.keep_last": 10
}`)
	if _, err = store.SaveVersion(ctx, threadPage, ownerConfig, "", author, "two auto values"); err != nil {
		t.Fatalf("save with two 'auto' values: %v", err)
	}
}

// TestSaveVersionStillRejectsANumberOutOfRange proves the fix did not weaken the check: the
// numeric bound of a oneOf branch is still enforced by the database.
func TestSaveVersionStillRejectsANumberOutOfRange(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	// tp.wrk.per_tab must be "auto" or an integer between 1 and 4; 9 is out of range.
	bad := json.RawMessage(`{
  "tp.wrk.per_tab": 9,
  "tp.wrk.min": 1,
  "tp.wrk.max": 4,
  "tp.ver.hash_check": true,
  "tp.ver.rerun_percent": 1,
  "tp.ver.signed_envelope": true,
  "tp.ver.canary": true,
  "tp.p2p.enabled": true,
  "tp.dew.enabled": true,
  "tp.dew.window_seconds": 86400,
  "tp.met.light": true,
  "cfg.keep_last": 10
}`)
	if _, err := store.SaveVersion(ctx, threadPage, bad, "", author, "out of range"); err == nil {
		t.Fatal("a workers-per-tab value of 9 was accepted although the schema allows 1 to 4")
	}
}

// TestMovingAJobReleasesTheOldVersion is the regression test of DDL BUG-1: when a job moves from
// version 1 to version 2, the in_use flag of version 1 must be cleared. The old trigger only
// recomputed the flag of the version named by COALESCE(NEW, OLD), so version 1 stayed flagged
// in_use=true for ever although no job_config_ref row pointed at it any more.
func TestMovingAJobReleasesTheOldVersion(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	first, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "first")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	second, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "second")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err = store.AttachJob(ctx, jobID, threadPage, first.Version); err != nil {
		t.Fatalf("attach to version %d: %v", first.Version, err)
	}
	if inUse, err := store.IsInUse(ctx, threadPage, first.Version); err != nil || !inUse {
		t.Fatalf("version %d is not in use after attach: %v %v", first.Version, inUse, err)
	}

	// Move the job to the newer version.
	if err = store.AttachJob(ctx, jobID, threadPage, second.Version); err != nil {
		t.Fatalf("move the job to version %d: %v", second.Version, err)
	}

	inUseOld, err := store.IsInUse(ctx, threadPage, first.Version)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if inUseOld {
		t.Fatalf("version %d still carries in_use=true although the job moved to version %d",
			first.Version, second.Version)
	}
	inUseNew, err := store.IsInUse(ctx, threadPage, second.Version)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if !inUseNew {
		t.Fatalf("version %d does not carry in_use=true although the job uses it", second.Version)
	}

	// The old version may be pruned now: no job keeps it.
	if err = store.DetachJob(ctx, jobID); err != nil {
		t.Fatalf("detach: %v", err)
	}
}

// TestSchemaBumpDoesNotFreezeTheInUseFlag is the regression test of DDL BUG-2: when the admin
// registers a newer schema version, a job must still be able to release its config version.
// The old guard demanded that the row carried the registered schema version on UPDATE as well
// as on INSERT, so the UPDATE inside the job_config_ref trigger was rejected, the job_config_ref
// row survived, the version stayed in_use=true and could never be pruned.
func TestSchemaBumpDoesNotFreezeTheInUseFlag(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err = store.AttachJob(ctx, jobID, threadPage, saved.Version); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// The admin registers a newer schema version (the document of the page keeps its history).
	var registered string
	if err = pool.QueryRow(ctx,
		`SELECT schema_version FROM pool_config_json_schema WHERE page = $1 AND is_current`,
		threadPage).Scan(&registered); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO pool_config_json_schema (page, schema_name, schema_version, schema, is_current)
		SELECT page, schema_name, '9.9.9', schema, true FROM pool_config_json_schema
		 WHERE page = $1 AND is_current`, threadPage); err != nil {
		t.Fatalf("cannot register the new schema version: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM pool_config_json_schema WHERE page = $1 AND schema_version = '9.9.9'`,
			threadPage); err != nil {
			t.Errorf("cannot remove the test schema version: %v", err)
		}
		if _, err := pool.Exec(context.Background(),
			`UPDATE pool_config_json_schema SET is_current = true
			  WHERE page = $1 AND schema_version = $2`, threadPage, registered); err != nil {
			t.Errorf("cannot restore the schema version: %v", err)
		}
	})

	// The job must be able to release its version after the schema bump.
	if err = store.DetachJob(ctx, jobID); err != nil {
		t.Fatalf("detach after the schema bump: %v", err)
	}

	var refs int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM job_config_ref WHERE job_id = $1`,
		jobID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatalf("%d job_config_ref rows survived the detach", refs)
	}
	inUse, err := store.IsInUse(ctx, threadPage, saved.Version)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if inUse {
		t.Fatalf("version %d is still in use although the job released it", saved.Version)
	}
}

// TestDatabaseSchemaCheckAcceptsAutoAndEnforcesTheBounds calls the database check of the
// registered schema directly, so the fix of the PostgreSQL 18 failure is proved on the SQL
// side and not only through the Go validator: the word "auto" must pass the oneOf check and a
// number outside the branch bounds must still be refused.
func TestDatabaseSchemaCheckAcceptsAutoAndEnforcesTheBounds(t *testing.T) {
	_, pool := newStore(t)
	ctx := context.Background()

	var oneOf json.RawMessage
	if err := pool.QueryRow(ctx, `
		SELECT schema->'properties'->'tp.wrk.per_tab'->'oneOf'
		  FROM pool_config_json_schema WHERE page = $1 AND is_current`, threadPage).Scan(&oneOf); err != nil {
		t.Fatalf("cannot read the registered schema: %v", err)
	}

	cases := []struct {
		value string
		want  bool
		why   string
	}{
		{`"auto"`, true, "the word auto is one of the allowed forms"},
		{`2`, true, "a number inside the bounds is allowed"},
		{`1`, true, "the lower bound itself is allowed"},
		{`4`, true, "the upper bound itself is allowed"},
		{`0`, false, "a number below the minimum is refused"},
		{`9`, false, "a number above the maximum is refused"},
		{`"two"`, false, "a word that is not auto is refused"},
	}
	for _, c := range cases {
		var got bool
		if err := pool.QueryRow(ctx,
			`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, c.value, oneOf).Scan(&got); err != nil {
			t.Fatalf("pool_config_one_of_ok(%s): %v", c.value, err)
		}
		if got != c.want {
			t.Fatalf("pool_config_one_of_ok(%s) = %v, want %v (%s)", c.value, got, c.want, c.why)
		}
	}

	// The whole page config with "auto" must pass the database check of the registered schema.
	if _, err := pool.Exec(ctx,
		`SELECT pool_config_validate_schema($1, $2::jsonb)`, threadPage, validThreadConfig); err != nil {
		t.Fatalf("the database refused a config with tp.wrk.per_tab=auto: %v", err)
	}
}

// TestTheDatabaseOneOfMeansExactlyOneBranch proves the rule the review asked for: a branch with
// neither a const nor a type must not match everything, and a value that matches two branches
// must be refused.
func TestTheDatabaseOneOfMeansExactlyOneBranch(t *testing.T) {
	_, pool := newStore(t)
	ctx := context.Background()

	// a branch with only an enum: the value must be inside the list
	enumOnly := json.RawMessage(`[{"enum":["a","b"]}]`)
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `"a"`, enumOnly).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a value inside the enum of the branch was refused")
	}
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `"z"`, enumOnly).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a value outside the enum of the branch was accepted")
	}

	// a branch with nothing at all matches nothing (fail closed)
	empty := json.RawMessage(`[{}]`)
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `"anything"`, empty).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a branch without a keyword accepted every value")
	}

	// a value that matches two branches is refused
	twoBranches := json.RawMessage(`[{"type":"integer"},{"type":"number"}]`)
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `5`, twoBranches).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a value that matches two branches was accepted")
	}
}

// TestTheDatabaseNeverCastsAConfigValueToNumeric is the root-cause guard of the PostgreSQL 18
// failure of WO T6.2, written as a BEHAVIOUR test and not as a grep of the source: the old
// validator cast the value of a schema property to numeric so it could compare it with the
// minimum and the maximum of a oneOf branch. A property may hold the word "auto" instead of a
// number, and PostgreSQL 18 no longer evaluates the type check lazily: the planner folds the
// cast out of the guard while it plans the SQL function, so the cast ran on the word "auto" and
// raised "invalid input syntax for type numeric: \"auto\"" (SQLSTATE 22P02).
//
// The behaviour that is proved here: the value "auto" survives the oneOf check, a number inside
// the bounds passes, a number outside the bounds is refused, and a value that is not a number
// is never cast (the check returns false instead of raising).
func TestTheDatabaseNeverCastsAConfigValueToNumeric(t *testing.T) {
	_, pool := newStore(t)
	ctx := context.Background()

	var oneOf json.RawMessage
	if err := pool.QueryRow(ctx, `
		SELECT schema->'properties'->'tp.wrk.per_tab'->'oneOf'
		  FROM pool_config_json_schema WHERE page = $1 AND is_current`, threadPage).Scan(&oneOf); err != nil {
		t.Fatal(err)
	}

	// the word "auto" must not raise a cast error
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `"auto"`, oneOf).Scan(&ok); err != nil {
		t.Fatalf("the oneOf check raised on the word auto: %v", err)
	}
	if !ok {
		t.Fatal("the word auto was refused")
	}

	// the bounds of the branch are still enforced
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `9`, oneOf).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a number above the maximum of the branch was accepted")
	}

	// a value that is not a number at all is not cast: the check answers false
	if err := pool.QueryRow(ctx,
		`SELECT pool_config_one_of_ok($1::jsonb, $2::jsonb)`, `true`, oneOf).Scan(&ok); err != nil {
		t.Fatalf("the oneOf check raised on a boolean: %v", err)
	}
	if ok {
		t.Fatal("a boolean was accepted by the oneOf check of a number")
	}
}

// ---------------------------------------------------------------------------
// WO FIX-E: the concurrency tests
// ---------------------------------------------------------------------------

// TestTwoSavesAtOnceGetDenseVersions is the concurrency test of the save path: two sessions
// that save at the same instant must get two different version numbers and no version number
// may be lost.
func TestTwoSavesAtOnceGetDenseVersions(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	const writers = 8
	var wg sync.WaitGroup
	versions := make([]int, writers)
	errs := make([]error, writers)

	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
				fmt.Sprintf("concurrent save %d", i))
			if err != nil {
				errs[i] = err
				return
			}
			versions[i] = v.Version
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("the concurrent save %d failed: %v", i, err)
		}
	}
	seen := map[int]bool{}
	for _, v := range versions {
		if v == 0 {
			t.Fatal("a concurrent save returned no version")
		}
		if seen[v] {
			t.Fatalf("two concurrent saves got the version %d", v)
		}
		seen[v] = true
	}
	// the versions are dense: 1 to writers, with no hole
	for i := 1; i <= writers; i++ {
		if !seen[i] {
			t.Fatalf("the version %d is missing: the numbers are not dense", i)
		}
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pool_config_version WHERE page = $1`, threadPage).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != writers {
		t.Fatalf("%d versions were written, want %d", count, writers)
	}
}

// TestPruneAndAttachAtOnceDoNotBreak proves that a prune and an attach of the same page that
// run at the same time do not break each other: the version that the job takes is never one
// that the prune deletes, and no call fails with a foreign key error.
func TestPruneAndAttachAtOnceDoNotBreak(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	// 12 versions, a retention of 2, so the prune has something to remove
	if err := store.SetRetention(ctx, threadPage, 2, author); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	var versions []int
	for i := 0; i < 12; i++ {
		v, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		versions = append(versions, v.Version)
	}
	oldest := versions[0]

	var wg sync.WaitGroup
	var pruneErr, attachErr error
	start := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, pruneErr = store.Prune(ctx, threadPage, author)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
		attachErr = store.AttachJob(ctx, jobID, threadPage, oldest)
	}()
	close(start)
	wg.Wait()

	if pruneErr != nil {
		t.Fatalf("the prune failed against the attach: %v", pruneErr)
	}

	// The lock of the page serializes the prune and the attach, so there are exactly two
	// legitimate outcomes:
	//   - the attach goes first: the version carries a job reference, the prune skips it
	//     (it is in use) and the attach reports success;
	//   - the prune goes first: the version is gone and the attach reports that there is no
	//     such version.
	// What must NEVER happen: a foreign key error (23503) in the prune, a prune that aborts,
	// or an attach that reports success for a version that has no job reference.
	if attachErr != nil && !errors.Is(attachErr, ErrVersionNotFound) {
		t.Fatalf("the attach failed against the prune with a real error: %v", attachErr)
	}

	if errors.Is(attachErr, ErrVersionNotFound) {
		// the prune won the race: the version is really gone
		var refs int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM job_config_ref j JOIN pool_config_version v ON v.id = j.config_version_id
			  WHERE v.page = $1 AND v.version = $2`, threadPage, oldest).Scan(&refs); err != nil {
			t.Fatal(err)
		}
		if refs != 0 {
			t.Fatalf("%d job references point at a version that was pruned", refs)
		}
		return
	}

	// The attach won the race: the version is in use and its reference is there.
	inUse, err := store.IsInUse(ctx, threadPage, oldest)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if !inUse {
		t.Fatal("the attach reported success but the version is not in use")
	}
	var refs int
	if err = pool.QueryRow(ctx,
		`SELECT count(*) FROM job_config_ref j JOIN pool_config_version v ON v.id = j.config_version_id
		  WHERE v.page = $1 AND v.version = $2`, threadPage, oldest).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("%d job references point at the version, want 1", refs)
	}
}

// TestTwoDetachesAtOnceLeaveTheVersionFree is the concurrency test of the in_use flag: two
// sessions that release the last two jobs of one version at the same time must leave the
// version free. Before, the READ COMMITTED write skew left the flag true for ever.
func TestTwoDetachesAtOnceLeaveTheVersionFree(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "save")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	jobA := fmt.Sprintf("s1.pool1.joba%d", time.Now().UnixNano())
	jobB := fmt.Sprintf("s1.pool1.jobb%d", time.Now().UnixNano())
	if err = store.AttachJob(ctx, jobA, threadPage, saved.Version); err != nil {
		t.Fatalf("attach a: %v", err)
	}
	if err = store.AttachJob(ctx, jobB, threadPage, saved.Version); err != nil {
		t.Fatalf("attach b: %v", err)
	}
	if inUse, err := store.IsInUse(ctx, threadPage, saved.Version); err != nil || !inUse {
		t.Fatalf("the version is not in use: %v %v", inUse, err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, jobID := range []string{jobA, jobB} {
		wg.Add(1)
		go func(i int, jobID string) {
			defer wg.Done()
			<-start
			errs[i] = store.DetachJob(ctx, jobID)
		}(i, jobID)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("the concurrent detach %d failed: %v", i, err)
		}
	}

	// The version must be free now: no job keeps it.
	inUse, err := store.IsInUse(ctx, threadPage, saved.Version)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if inUse {
		t.Fatal("the version is still in use although both jobs were released")
	}
	var refs int
	if err = pool.QueryRow(ctx,
		`SELECT count(*) FROM job_config_ref WHERE config_version_id = $1`, saved.ID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatalf("%d job references survived the detaches", refs)
	}
}

// TestAttachJobLatestTakesTheNewestVersion proves the one-statement attach: the job takes the
// newest version of the page, and the version is really kept (it may not be pruned away).
func TestAttachJobLatestTakesTheNewestVersion(t *testing.T) {
	store, pool := newStore(t)
	cleanPage(t, pool, threadPage)
	ctx := context.Background()
	author := adminUser(t, pool)

	for i := 0; i < 3; i++ {
		if _, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author,
			fmt.Sprintf("save %d", i)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	if err := store.AttachJobLatest(ctx, jobID, threadPage); err != nil {
		t.Fatalf("attach the newest version: %v", err)
	}
	latest, err := store.Latest(ctx, threadPage)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	inUse, err := store.IsInUse(ctx, threadPage, latest.Version)
	if err != nil {
		t.Fatalf("is in use: %v", err)
	}
	if !inUse {
		t.Fatal("the newest version is not in use although the job took it")
	}
}

// TestEveryCallLogsOneDebugLine drives every method and every outcome of the store and proves
// that each call writes exactly one debug line. An empty buffer is a failure, not a pass.
func TestEveryCallLogsOneDebugLine(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pool, err := pgxpool.New(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("cannot connect: %v", err)
	}
	defer pool.Close()
	store := NewConfigStore(pool, logger)
	ctx := context.Background()
	author := adminUser(t, pool)
	cleanPage(t, pool, threadPage)

	jobID := fmt.Sprintf("s1.pool1.job%d", time.Now().UnixNano())
	saved, err := store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "log test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	cases := []struct {
		name string
		call func()
	}{
		{"save", func() { _, _ = store.SaveVersion(ctx, threadPage, validThreadConfig, "", author, "log") }},
		{"save rejected", func() { _, _ = store.SaveVersion(ctx, "no-such-page", validThreadConfig, "", author, "x") }},
		{"save rejected by the schema", func() { _, _ = store.SaveVersion(ctx, threadPage, json.RawMessage(`{}`), "", author, "x") }},
		{"latest", func() { _, _ = store.Latest(ctx, threadPage) }},
		{"latest rejected", func() { _, _ = store.Latest(ctx, "no-such-page") }},
		{"get", func() { _, _ = store.Get(ctx, threadPage, saved.Version) }},
		{"get missing", func() { _, _ = store.Get(ctx, threadPage, 999999) }},
		{"list", func() { _, _ = store.List(ctx, threadPage, 0) }},
		{"list with a negative limit", func() { _, _ = store.List(ctx, threadPage, -1) }},
		{"pin", func() { _ = store.Pin(ctx, threadPage, saved.Version, "log-pin", author) }},
		{"pin rejected", func() { _ = store.Pin(ctx, threadPage, saved.Version, "  ", author) }},
		{"unpin", func() { _ = store.Unpin(ctx, threadPage, saved.Version, author) }},
		{"unpin of a version that is not pinned", func() { _ = store.Unpin(ctx, threadPage, saved.Version, author) }},
		{"attach", func() { _ = store.AttachJob(ctx, jobID, threadPage, saved.Version) }},
		{"attach rejected", func() { _ = store.AttachJob(ctx, "bad id", threadPage, saved.Version) }},
		{"attach the newest version", func() { _ = store.AttachJobLatest(ctx, jobID, threadPage) }},
		{"detach", func() { _ = store.DetachJob(ctx, jobID) }},
		{"detach of a job that does not exist", func() { _ = store.DetachJob(ctx, "s1.pool1.nosuchjob") }},
		{"is in use", func() { _, _ = store.IsInUse(ctx, threadPage, saved.Version) }},
		{"is in use of a missing version", func() { _, _ = store.IsInUse(ctx, threadPage, 999999) }},
		{"prune", func() { _, _ = store.Prune(ctx, threadPage, author) }},
		{"prune of an unknown page", func() { _, _ = store.Prune(ctx, "no-such-page", author) }},
		{"retention", func() { _, _ = store.Retention(ctx, threadPage) }},
		{"set retention", func() { _ = store.SetRetention(ctx, threadPage, 10, author) }},
		{"set retention refused", func() { _ = store.SetRetention(ctx, threadPage, 0, author) }},
		{"rollback", func() { _, _ = store.Rollback(ctx, threadPage, saved.Version, "log rollback", author) }},
		{"rollback without a reason", func() { _, _ = store.Rollback(ctx, threadPage, saved.Version, "  ", author) }},
		{"rollback of a missing version", func() { _, _ = store.Rollback(ctx, threadPage, 999999, "log", author) }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf.Reset()
			c.call()
			out := strings.TrimSpace(buf.String())
			if out == "" {
				t.Fatal("the call wrote no log line at all")
			}
			lines := strings.Count(out, "\n") + 1
			if lines != 1 {
				t.Fatalf("the call wrote %d log lines, want exactly 1:\n%s", lines, out)
			}
		})
	}
}
