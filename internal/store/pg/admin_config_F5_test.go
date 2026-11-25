// Path: internal/store/pg/admin_config_F5_test.go | Codex | 2026-11-18
// T42 F5: real PostgreSQL atomic saves, pins and job transaction references.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
)

func TestF5AdminConfigTransactions(t *testing.T) {
	c, db := newStore(t)
	ctx := context.Background()
	author := adminUser(t, db)
	validate := func(json.RawMessage) error { return nil }
	var username string
	if err := db.QueryRow(ctx, `SELECT username FROM admin_user WHERE id=$1`, author).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if got, err := c.AdminAuthor(ctx, username); err != nil || got != author {
		t.Fatalf("actor %d %v", got, err)
	}
	if _, err := c.AdminAuthor(ctx, "missing-f5-actor"); !errors.Is(err, ErrAuthorRequired) {
		t.Fatalf("missing actor %v", err)
	}
	latest := 0
	if v, err := c.Latest(ctx, threadPage); err == nil {
		latest = v.Version
	} else if !errors.Is(err, ErrVersionNotFound) {
		t.Fatal(err)
	}
	q := AdminConfigWrite{Settings: validThreadConfig, Reason: "F5 atomic save", ExpectedVersion: &latest}
	v, err := c.WriteAdminConfig(ctx, threadPage, q, author, validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.WriteAdminConfig(ctx, threadPage, q, author, validate); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale save %v", err)
	}
	empty := ""
	q.ExpectedVersion = &v.Version
	q.PinName = &empty
	if _, err = c.WriteAdminConfig(ctx, threadPage, q, author, validate); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid pin %v", err)
	}
	current, err := c.Latest(ctx, threadPage)
	if err != nil || current.Version != v.Version {
		t.Fatalf("failed save committed %+v %v", current, err)
	}
	q = AdminConfigWrite{RollbackVersion: v.Version, Reason: "F5 rollback", ExpectedVersion: &v.Version}
	restored, err := c.WriteAdminConfig(ctx, threadPage, q, author, validate)
	if err != nil || restored.Version <= v.Version {
		t.Fatalf("rollback %+v %v", restored, err)
	}
	jobID := fmt.Sprintf("f5.tp.j%d", time.Now().UnixNano())
	wrapped := &DB{Pool: db}
	sentinel := errors.New("F5 rollback job")
	var snapshot struct {
		Page    string
		Version int
		Config  json.RawMessage
	}
	err = wrapped.Update(ctx, func(tx store.Tx) error {
		if err := tx.Get(store.ConfigVersions, "active:"+threadPage, &snapshot); err != nil {
			return err
		}
		if snapshot.Version != restored.Version {
			return fmt.Errorf("wrong active version %d", snapshot.Version)
		}
		if err := tx.Put(store.ConfigVersions, "job:"+jobID, snapshot); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM job_config_ref WHERE job_id=$1`, jobID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled-back ref count=%d %v", n, err)
	}
	err = wrapped.Update(ctx, func(tx store.Tx) error {
		if err := tx.Get(store.ConfigVersions, "active:"+threadPage, &snapshot); err != nil {
			return err
		}
		return tx.Put(store.ConfigVersions, "job:"+jobID, snapshot)
	})
	if err != nil {
		t.Fatal(err)
	}
	if used, err := c.IsInUse(ctx, threadPage, restored.Version); err != nil || !used {
		t.Fatalf("pin missing %v %v", used, err)
	}
	if err = c.DetachJob(ctx, jobID); err != nil {
		t.Fatal(err)
	}
}
