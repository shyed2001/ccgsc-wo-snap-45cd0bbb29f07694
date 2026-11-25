package grpcapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"github.com/jackc/pgx/v5"
)

// R3-01: reproduce the production constructor's event bus wrapping the real
// PostgreSQL transaction, including first-write CAS and stale version rejection.
func TestR3AFirstRecomputeWriteThroughGRPCNew(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		if os.Getenv("CI") == "true" || os.Getenv("CI") == "1" {
			t.Fatal("POOLS_PG_DSN is not set in CI")
		}
		t.Skip("POOLS_PG_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse integration-test DSN failed")
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect integration-test database failed")
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := admin.Close(c); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "r3a_grpc_" + hex.EncodeToString(nonce[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("cleanup private schema: %v", err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse scoped DSN failed")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	scoped, err := pgx.ParseConfig(dsn)
	if err != nil || scoped.RuntimeParams["search_path"] != schema {
		t.Fatal("unsafe private search_path")
	}
	st, err := pg.Open(dsn)
	if err != nil {
		t.Fatal("open scoped PostgreSQL store failed")
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close private store: %v", err)
		}
	})
	db, ok := st.(*pg.DB)
	if !ok {
		t.Fatal("not a PostgreSQL store")
	}
	var actual string
	if err := db.Pool.QueryRow(ctx, "SELECT current_schema()").Scan(&actual); err != nil || actual != schema {
		t.Fatalf("unexpected test schema: %q %v", actual, err)
	}
	engine, err := pool.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "r3a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := New(engine, nil)
	zero := 0
	mode := pool.RecomputeModeRegistry
	saved, err := service.Engine.CommitRecomputeSettings(ctx, pool.Thread, pool.RecomputeSettingsDelta{Mode: &mode}, &zero, "r3a-first")
	if err != nil {
		t.Fatalf("first write through grpcapi.New: %v", err)
	}
	if saved.Version != 1 || saved.Mode != mode || saved.BusyPolicy != pool.DefaultRecomputeBusyPolicy || saved.MalformedPolicy != pool.DefaultRecomputeMalformedPolicy {
		t.Fatalf("unexpected settings: %+v", saved)
	}
	if _, err := service.Engine.CommitRecomputeSettings(ctx, pool.Thread, pool.RecomputeSettingsDelta{}, &zero, "r3a-stale"); !errors.Is(err, pool.ErrSettingsConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if err := st.View(ctx, func(r store.Reader) error {
		var audit map[string]any
		return r.Get(store.Audit, engine.RecomputeSettingsAuditKey(pool.Thread, 1), &audit)
	}); err != nil {
		t.Fatalf("audit missing: %v", err)
	}
	if err := service.Engine.Store.Update(ctx, func(tx store.Tx) error {
		atomic, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return errors.New("missing forwarded interface")
		}
		if inserted, err := atomic.PutIfAbsent(store.Events, "r3a-forbidden", map[string]string{"test": "must not publish"}); inserted || !errors.Is(err, store.ErrNotImplemented) {
			return errors.New("events bucket bypass accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
