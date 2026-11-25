package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
)

func TestStoreCRUDAndResultUniqueness(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		if isCI() {
			t.Fatal("POOLS_PG_DSN is not set and CI is set")
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL store test")
	}

	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	key := fmt.Sprintf("f4-%d", time.Now().UnixNano())
	value := map[string]any{"jobId": "f4.job", "state": "running"}
	if err = db.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Jobs, key, value)
	}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = db.View(ctx, func(rd store.Reader) error {
		return rd.Get(store.Jobs, key, &got)
	}); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "running" {
		t.Fatalf("stored value = %#v", got)
	}

	taskKey := key + "-a"
	result := map[string]any{"jobId": key, "taskId": taskKey, "status": "done"}
	if err = db.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Results, "first-"+key, result)
	}); err != nil {
		t.Fatal(err)
	}
	err = db.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Results, "second-"+key, result)
	})
	if err == nil {
		t.Fatal("duplicate (job_id, task_id) result was accepted")
	}

	callbackErr := errors.New("force rollback")
	if err = db.Update(ctx, func(tx store.Tx) error {
		if err := tx.Put(store.Audit, key, map[string]string{"event": "rollback-me"}); err != nil {
			return err
		}
		return callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("rollback callback error = %v", err)
	}
	if err = db.View(ctx, func(rd store.Reader) error {
		var audit map[string]string
		err := rd.Get(store.Audit, key, &audit)
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("rollback record visible: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorePreservesRawJSONBytes(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		if isCI() {
			t.Fatal("POOLS_PG_DSN is not set and CI is set")
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL store test")
	}

	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Only store.StoredJSONMarshaler values keep exact bytes (T53); plain json.Marshal compacts RawMessage.
	want := json.RawMessage(`{"b":2, "a":1}`)
	key := fmt.Sprintf("raw-%d", time.Now().UnixNano())
	if err = db.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.Jobs, key, rawRecord{Output: want})
	}); err != nil {
		t.Fatal(err)
	}
	var got rawRecord
	if err = db.View(context.Background(), func(rd store.Reader) error {
		return rd.Get(store.Jobs, key, &got)
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Output, want) {
		t.Fatalf("raw output = %q, want %q", got.Output, want)
	}
}

// rawRecord keeps its output bytes verbatim through MarshalStoredJSON, like pool results do.
type rawRecord struct {
	Output json.RawMessage `json:"output"`
}

func (r rawRecord) MarshalStoredJSON() ([]byte, error) {
	return append(append([]byte(`{"output":`), r.Output...), '}'), nil
}

func TestStoreSerializesConcurrentIdempotencyAdmission(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN"))
	if dsn == "" {
		if isCI() {
			t.Fatal("POOLS_PG_DSN is not set and CI is set")
		}
		t.Skip("POOLS_PG_DSN is not set; skipping PostgreSQL store test")
	}

	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("idem-%d", time.Now().UnixNano())
	firstReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	var wg sync.WaitGroup
	var firstID, secondID string
	var firstErr, secondErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		firstErr = db.Update(ctx, func(tx store.Tx) error {
			var prior struct {
				Hash  string
				JobID string
			}
			if err := tx.Get(store.Idempotency, key, &prior); !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("first lookup: %w", err)
			}
			close(firstReady)
			<-releaseFirst
			firstID = "job-first"
			return tx.Put(store.Idempotency, key, struct {
				Hash  string
				JobID string
			}{"same-request", firstID})
		})
	}()

	<-firstReady
	wg.Add(1)
	go func() {
		defer wg.Done()
		secondErr = db.Update(ctx, func(tx store.Tx) error {
			var prior struct {
				Hash  string
				JobID string
			}
			err := tx.Get(store.Idempotency, key, &prior)
			if errors.Is(err, store.ErrNotFound) {
				secondID = "job-second"
				return tx.Put(store.Idempotency, key, struct {
					Hash  string
					JobID string
				}{"same-request", secondID})
			}
			if err != nil {
				return err
			}
			secondID = prior.JobID
			return nil
		})
	}()
	close(releaseFirst)
	wg.Wait()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("concurrent idempotency writes: first=%v second=%v", firstErr, secondErr)
	}
	if firstID != "job-first" || secondID != firstID {
		t.Fatalf("idempotency IDs = first %q, second %q; want both %q", firstID, secondID, firstID)
	}
}
