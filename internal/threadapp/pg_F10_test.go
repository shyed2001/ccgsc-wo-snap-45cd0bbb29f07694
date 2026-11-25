package threadapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"github.com/jackc/pgx/v5"
)

func f10SharedPG(t *testing.T) (store.Store, store.Store) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if raw == "" {
		t.Skip("POOLS_PG_DSN required for F9 cross-instance test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("f10_app_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, _ = conn.Exec(c, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = conn.Close(c)
	})
	scoped := raw
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	} else {
		if strings.Contains(raw, "search_path=") {
			t.Fatal("F9 PG fixture rejects multiple search_path options")
		}
		scoped = raw + " search_path=" + schema
	}
	one, err := pg.Open(scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = one.Close() })
	two, err := pg.Open(scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = two.Close() })
	return one, two
}
func TestF10PGConcurrentMultiServerQuotaK1(t *testing.T) {
	db1, db2 := f10SharedPG(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 5, 20, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		appID := fmt.Sprintf("AppPG%d", i)
		if err := Approve(ctx, db1, "dev", appID, 1, now); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var success, full atomic.Int32
		for j, db := range []store.Store{db1, db2} {
			id := fmt.Sprintf("device%d:tab%d", i, j)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := Bind(ctx, db, "dev", appID, id, now, time.Minute)
				switch {
				case err == nil:
					success.Add(1)
				case errors.Is(err, ErrFull):
					full.Add(1)
				default:
					t.Errorf("unexpected PG concurrency outcome: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if success.Load() != 1 || full.Load() != 1 {
			t.Errorf("two PG instances claimed W=1 app: success=%d full=%d", success.Load(), full.Load())
		}
	}
}
