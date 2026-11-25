// Path: tests/review_go_test.go | Author: Codex | 2026-11-17
// What/why: review-01 upload, role, JWT and SSE regression tests.
package tests

import (
	"bufio"
	"bytes"
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewStandardUploadAndAdminRead(t *testing.T) {
	f := setup(t)
	_, dev := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	_, director := f.key(t, "director")
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		b := []byte("module fixture")
		id := verify.SHA256(b)
		f.request(t, "PUT", base+"/artifacts", dev, b, 201, map[string]string{"X-Artifact-Sha256": id})
		got := f.request(t, "GET", base+"/artifacts/"+id, admin, nil, 200, nil)
		if !bytes.Equal(b, got) {
			t.Fatal("admin received wrong artifact")
		}
		// R15: the director now reads artifacts alongside the admin (T100 already
		// exempts both from the developer owner-reference rule).
		f.request(t, "GET", base+"/artifacts/"+id, director, nil, 200, nil)
		f.request(t, "PUT", base+"/artifacts", dev, b, 400, map[string]string{"X-Artifact-Sha256": id, "Upload-Offset": "bad"})
		r := httptest.NewRequest("PUT", base+"/artifacts", bytes.NewReader(b))
		r.ContentLength = -1
		r.Header.Set("Authorization", "Bearer "+dev)
		r.Header.Set("X-Artifact-Sha256", id)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("unknown-length upload: %d", w.Code)
		}
	}
}
func TestReviewJWTIssuedAtSkewAndExpiry(t *testing.T) {
	f := setup(t)
	now := time.Unix(1_800_000_000, 0)
	f.auth.Now = func() time.Time { return now }
	raw, _ := f.key(t, "developer")
	for _, skew := range []time.Duration{120 * time.Second, 121 * time.Second} {
		f.auth.Now = func() time.Time { return now.Add(skew) }
		token, err := f.auth.Exchange(context.Background(), raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.auth.Now = func() time.Time { return now }
		_, err = f.auth.Validate(context.Background(), token.AccessToken)
		if (err == nil) != (skew == 120*time.Second) {
			t.Fatalf("skew %v: %v", skew, err)
		}
	}
	f.auth.Now = func() time.Time { return now }
	token, err := f.auth.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.auth.Now = func() time.Time { return now.Add(15 * time.Minute) }
	if _, err = f.auth.Validate(context.Background(), token.AccessToken); err == nil {
		t.Fatal("expired token accepted")
	}
}
func TestReviewSSERenewalBothRoutes(t *testing.T) {
	for _, route := range []struct{ path, role string }{{"/events", "developer"}, {"/workers/tab/tasks", "contributor"}} {
		for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
			f := setup(t)
			now := time.Now()
			f.auth.Now = func() time.Time { return now }
			_, token := f.key(t, route.role)
			f.auth.Now = func() time.Time { return now.Add(14*time.Minute + 40*time.Second) }
			r := httptest.NewRequest("GET", base+route.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "event: reauth\n") || !strings.Contains(w.Body.String(), "refresh_and_reconnect") {
				t.Fatalf("%s: %d %s", route.path, w.Code, w.Body.String())
			}
		}
	}
}

type eventCountingStore struct {
	store.Store
	lists atomic.Int32
}
type eventCountingReader struct {
	store.Reader
	owner *eventCountingStore
}

func (r eventCountingReader) List(bucket string) ([]store.Record, error) {
	if bucket == store.Events {
		r.owner.lists.Add(1)
	}
	return r.Reader.List(bucket)
}
func (s *eventCountingStore) View(ctx context.Context, fn func(store.Reader) error) error {
	return s.Store.View(ctx, func(r store.Reader) error { return fn(eventCountingReader{r, s}) })
}
func TestReviewSSECommitBroadcastReplayAndNoPollingScans(t *testing.T) {
	f := setup(t)
	counted := &eventCountingStore{Store: f.engine.Store}
	f.engine.Store = counted
	server := &api.Server{Engine: f.engine, Auth: f.auth, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	_, token := f.key(t, "developer")
	claims, err := f.auth.Validate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	put := func(id int, owner string, rollback bool) {
		t.Helper()
		err := f.engine.Store.Update(context.Background(), func(tx store.Tx) error {
			key := fmt.Sprintf("%020d", id)
			if e := tx.Put(store.Events, key, pool.Event{ID: key, Owner: owner, PoolID: pool.Thread, Type: "task.done", JobID: "job"}); e != nil {
				return e
			}
			if rollback {
				return errors.New("rollback fixture")
			}
			return nil
		})
		if !rollback && err != nil {
			t.Fatal(err)
		}
	}
	put(1, claims.KeyID, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connect := func(last string) (*http.Response, *bufio.Reader) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/thread-pool/v1/events?types=task.done", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Last-Event-ID", last)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("SSE status %d", resp.StatusCode)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	next := func(rd *bufio.Reader) string {
		t.Helper()
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "id: ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "id: "))
			}
		}
	}
	resp, reader := connect("")
	defer resp.Body.Close()
	if got := next(reader); got != fmt.Sprintf("%020d", 1) {
		t.Fatal(got)
	}
	resp2, reader2 := connect(fmt.Sprintf("%020d", 1))
	defer resp2.Body.Close()
	// Three former poll intervals must not rescan historical events.
	time.Sleep(800 * time.Millisecond)
	if n := counted.lists.Load(); n != 2 {
		t.Fatalf("expected one replay per stream, got %d full scans", n)
	}
	put(2, claims.KeyID, true) // rolled back records must never be broadcast
	put(3, "other-owner", false)
	put(4, claims.KeyID, false)
	for _, rd := range []*bufio.Reader{reader, reader2} {
		if got := next(rd); got != fmt.Sprintf("%020d", 4) {
			t.Fatalf("rollback/filter/live delivery: %s", got)
		}
	}
	if n := counted.lists.Load(); n != 2 {
		t.Fatalf("live events caused scans: %d", n)
	}
	if err := f.auth.Revoke(context.Background(), claims.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatalf("revocation did not close stream: %v", err)
	}
}

func TestReviewActiveSSERenewalBothRoutes(t *testing.T) {
	for _, role := range []string{"developer", "contributor"} {
		t.Run(role, func(t *testing.T) {
			f := setup(t)
			issued := time.Now().Truncate(time.Second)
			f.auth.Now = func() time.Time { return issued }
			_, token := f.key(t, role)
			path := "/thread-pool/v1/events"
			if role == "contributor" {
				w := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", token, map[string]any{"hostId": "renewal-test", "slots": 1}, 201, nil))
				path = "/thread-pool/v1/workers/" + w.TabID + "/tasks"
			}
			// Keep auth valid while the middleware's real timer cancels the active loop.
			f.auth.Now = func() time.Time { return issued.Add(15*time.Minute - 30*time.Second - 100*time.Millisecond) }
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), ": connected") || !strings.Contains(w.Body.String(), "event: reauth") {
				t.Fatalf("active stream did not renew: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
