// T42 F1, 2026-11-18: split/batch acceptance and lease lifecycle regression tests.
package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

func uploadF1(t *testing.T, e *Engine, data []byte) string {
	t.Helper()
	id := verify.SHA256(data)
	if _, err := e.Upload(context.Background(), id, "", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestF1SplitPartitions(t *testing.T) {
	for _, format := range []string{"bytes", "range"} {
		for _, tc := range []struct {
			name                 string
			length, count, chunk int
		}{
			{"one", 1, 1, 0}, {"N", 12, 3, 0}, {"balanced remainder", 10, 3, 0},
			{"chunk remainder", 10, 3, 4}, {"max", 10000, 10000, 1},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t)
				e := h.engine
				module := uploadF1(t, e, []byte("module"))
				data := bytes.Repeat([]byte("x"), tc.length)
				start := int64(0)
				if format == "range" {
					start = -7
					data = []byte(fmt.Sprintf(`{"start":%d,"end":%d}`, start, start+int64(tc.length)))
				}
				input := uploadF1(t, e, data)
				q := CreateRequest{ModuleArtifactIDs: []string{module}, Split: &SplitSpec{ArtifactID: input, Count: tc.count, Format: format, Chunksize: tc.chunk}}
				j, err := e.Create(context.Background(), Thread, "developer", "split", q)
				if err != nil {
					t.Fatal(err)
				}
				if j.Progress.Total != tc.count || len(j.Tasks) != tc.count || j.Request.Split == nil || len(j.Request.Tasks) != 0 {
					t.Fatalf("bad expansion: %+v", j.Progress)
				}
				next := start
				seen := map[string]bool{}
				for i, task := range j.Tasks {
					if seen[task.TaskID] {
						t.Fatal("duplicate ID")
					}
					seen[task.TaskID] = true
					var part SplitInput
					if err = json.Unmarshal(task.Input, &part); err != nil {
						t.Fatal(err)
					}
					lo, hi := part.Offset, part.Offset+part.Length
					if format == "range" {
						if part.Range == nil {
							t.Fatal("missing range")
						}
						lo, hi = part.Range.Start, part.Range.End
					}
					if part.ArtifactID != input || part.Format != format || lo != next || hi <= lo {
						t.Fatalf("bad slice %d: %+v; next %d", i, part, next)
					}
					if tc.chunk > 0 && i < tc.count-1 && hi-lo != int64(tc.chunk) {
						t.Fatal("wrong chunk size")
					}
					next = hi
				}
				if next != start+int64(tc.length) {
					t.Fatalf("lost remainder: %d", next)
				}
				again, err := e.Create(context.Background(), Thread, "developer", "split", q)
				if err != nil || again.JobID != j.JobID {
					t.Fatalf("idempotency: %v", err)
				}
				q.Split.Count++
				if _, err = e.Create(context.Background(), Thread, "developer", "split", q); err == nil {
					t.Fatal("changed request accepted")
				}
			})
		}
	}
}

func TestF1SplitValidation(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	id := uploadF1(t, e, []byte("0123456789"))
	for _, tc := range []struct {
		name   string
		split  SplitSpec
		chunks map[string]int
		batch  int
		want   error
	}{
		{"zero", SplitSpec{ArtifactID: id}, nil, 0, ErrInvalid},
		{"over max", SplitSpec{ArtifactID: id, Count: 10001}, nil, 0, ErrInvalid},
		{"negative chunk", SplitSpec{ArtifactID: id, Count: 1, Chunksize: -1}, nil, 0, ErrInvalid},
		{"bad id", SplitSpec{ArtifactID: "bad", Count: 1}, nil, 0, ErrInvalid},
		{"rows", SplitSpec{ArtifactID: id, Count: 1, Format: "rows"}, nil, 0, ErrUnsupported},
		{"records", SplitSpec{ArtifactID: id, Count: 1, Format: "records"}, nil, 0, ErrUnsupported},
		{"unknown", SplitSpec{ArtifactID: id, Count: 1, Format: "other"}, nil, 0, ErrUnsupported},
		{"conflicting chunks", SplitSpec{ArtifactID: id, Count: 3, Chunksize: 4}, map[string]int{id: 5}, 0, ErrInvalid},
		{"unhandled map", SplitSpec{ArtifactID: id, Count: 1}, map[string]int{"other": 2}, 0, ErrUnsupported},
		{"batch max", SplitSpec{ArtifactID: id, Count: 1}, nil, 10001, ErrInvalid},
		{"negative batch", SplitSpec{ArtifactID: id, Count: 1}, nil, -1, ErrInvalid},
		{"empty slices", SplitSpec{ArtifactID: id, Count: 11}, nil, 0, ErrInvalid},
		{"inconsistent count", SplitSpec{ArtifactID: id, Count: 2, Chunksize: 4}, nil, 0, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := CreateRequest{ModuleArtifactIDs: []string{id}, Split: &tc.split, Chunksize: tc.chunks, BatchSize: tc.batch}
			if _, err := e.Create(context.Background(), Thread, "dev", "", q); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Split: &SplitSpec{ArtifactID: id, Count: 3}, Chunksize: map[string]int{id: 4}}
	j, err := e.Create(context.Background(), Thread, "dev", "", q)
	if err != nil || len(j.Tasks) != 3 {
		t.Fatalf("chunksize map: %v", err)
	}
	q.Tasks = []TaskSpec{{TaskID: "inline"}}
	if _, _, err = e.Validate(q); !errors.Is(err, ErrInvalid) {
		t.Fatal("tasks+split accepted")
	}
	q.Split = nil
	if _, _, err = e.Validate(q); !errors.Is(err, ErrUnsupported) {
		t.Fatal("inline chunksize silently ignored")
	}
	q.Chunksize = nil
	q.Options = json.RawMessage(`{"unimplemented":true}`)
	if _, _, err = e.Validate(q); !errors.Is(err, ErrUnsupported) {
		t.Fatal("options silently ignored")
	}
	for _, data := range []string{`{}`, `{"start":0}`, `{"start":1,"end":1}`, `{"start":2,"end":1}`, `{"start":0.5,"end":2}`, `{"start":-9223372036854775808,"end":9223372036854775807}`, `no json`} {
		input := uploadF1(t, e, []byte(data))
		q := CreateRequest{ModuleArtifactIDs: []string{id}, Split: &SplitSpec{ArtifactID: input, Count: 1, Format: "range"}}
		if _, err := e.Create(context.Background(), Thread, "dev", "", q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("range %s: %v", data, err)
		}
	}
	missing := verify.SHA256([]byte("absent"))
	q = CreateRequest{ModuleArtifactIDs: []string{id}, Split: &SplitSpec{ArtifactID: missing, Count: 1}}
	if _, err := e.Create(context.Background(), Thread, "dev", "", q); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
	incomplete := []byte("incomplete")
	id2 := verify.SHA256(incomplete)
	if _, err := e.Upload(context.Background(), id2, "", 0, int64(len(incomplete)), incomplete[:1]); err != nil {
		t.Fatal(err)
	}
	q.Split.ArtifactID = id2
	if _, err := e.Create(context.Background(), Thread, "dev", "", q); !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete source: %v", err)
	}
}

func batchJobF1(t *testing.T, h *harness, n, batch int, opts ...string) Job {
	t.Helper()
	id := uploadF1(t, h.engine, []byte("module"))
	zero := 0.0
	q := CreateRequest{ModuleArtifactIDs: []string{id}, BatchSize: batch, RerunPercent: &zero}
	if len(opts) > 0 && opts[0] != "" {
		q.Options = json.RawMessage(opts[0])
	}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: fmt.Sprint(i), Input: json.RawMessage(`{"n":1}`)})
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", q)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func submitF1(t *testing.T, e *Engine, w Worker, a Assignment) {
	t.Helper()
	output := json.RawMessage(`{"ok":true}`)
	_, err := e.Submit(context.Background(), w.PoolID, w.Owner, w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: output, SHA256: verify.SHA256(output)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestF1BatchRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		n, batch int
		sizes    []int
	}{{1, 0, []int{1}}, {1, 1, []int{1}}, {5, 2, []int{2, 2, 1}}, {3, 10000, []int{3}}} {
		t.Run(fmt.Sprintf("%d/%d", tc.n, tc.batch), func(t *testing.T) {
			h := newHarness(t)
			e := h.engine
			j := batchJobF1(t, h, tc.n, tc.batch)
			w := h.worker(t, max(1, min(tc.batch, tc.n)))
			var prev string
			for _, want := range tc.sizes {
				a, err := e.Claim(context.Background(), Thread, w.Owner, w.TabID)
				if err != nil {
					t.Fatal(err)
				}
				members := a.Batch
				if tc.batch <= 1 {
					if len(members) != 0 {
						t.Fatal("single-task wire changed")
					}
					members = []Assignment{a}
				}
				if len(members) != want {
					t.Fatalf("members %d want %d", len(members), want)
				}
				if prev == a.Lease.Token {
					t.Fatal("reused batch token")
				}
				prev = a.Lease.Token
				for i, m := range members {
					if m.Lease.Token != a.Lease.Token || len(m.Batch) != 0 || !verify.VerifyEnvelope(m.Envelope, e.Signer.Public) {
						t.Fatal("bad member")
					}
					if i == 0 {
						if _, err = e.Claim(context.Background(), Thread, w.Owner, w.TabID); !errors.Is(err, ErrEmpty) {
							t.Fatalf("slot freed before member %d: %v", i, err)
						}
					}
					submitF1(t, e, w, m)
				}
			}
			got, err := e.Get(context.Background(), Thread, "developer", j.JobID)
			if err != nil || got.State != "completed" || got.Progress.Completed != tc.n {
				t.Fatalf("completion: %+v %v", got.Progress, err)
			}
		})
	}
}

func TestF1BatchRenewExpiryAndFencing(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	// This test is the heartbeat-lease fence (renew, then reclaim at TTL).
	// Dew would hold the tasks for 10 minutes (G-F12-5), so it is off here.
	j := batchJobF1(t, h, 3, 3, `{"dew":{"enabled":false}}`)
	w := h.worker(t, 3)
	a, err := e.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(30 * time.Second)
	forged := a.Lease
	forged.Generation++
	if _, err = e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &forged); !errors.Is(err, ErrConflict) {
		t.Fatal("forged renewal accepted")
	}
	if _, err = e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Lease); err != nil {
		t.Fatal(err)
	}
	got, err := e.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range got.Tasks {
		if !task.Lease.ExpiresAt.Equal(h.now.Add(e.TTL)) {
			t.Fatal("member not renewed")
		}
	}
	h.now = h.now.Add(40 * time.Second) // beyond original signed deadline, within renewal
	submitF1(t, e, w, a.Batch[0])
	h.now = h.now.Add(21 * time.Second)
	if _, err = e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Batch[1].Lease); !errors.Is(err, ErrConflict) {
		t.Fatal("expired member revived")
	}
	if _, err = e.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, nil); err != nil {
		t.Fatal(err)
	}
	retry, err := e.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Batch) != 2 || retry.Lease.Token == a.Lease.Token {
		t.Fatal("bad retry batch")
	}
	for _, member := range retry.Batch {
		if member.Lease.Generation != 2 || member.Lease.Attempt != 2 {
			t.Fatal("lost fence")
		}
	}
	output := json.RawMessage(`{}`)
	stale := a.Batch[1]
	if _, err = e.Submit(context.Background(), Thread, w.Owner, w.TabID, Submission{Lease: stale.Lease, TaskEnvelope: stale.TaskEnvelope, LeaseReceipt: stale.LeaseReceipt, Output: output, SHA256: verify.SHA256(output)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale result: %v", err)
	}
	for _, member := range retry.Batch {
		submitF1(t, e, w, member)
	}
}

func TestF1ConcurrentBatchesUseSlots(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	batchJobF1(t, h, 12, 3)
	w := h.worker(t, 6)
	var wg sync.WaitGroup
	results := make(chan Assignment, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := e.Claim(context.Background(), Thread, w.Owner, w.TabID)
			if err != nil {
				errs <- err
			} else {
				results <- a
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 2 {
		t.Fatalf("claims=%d want 2", len(results))
	}
	seen := map[string]bool{}
	for a := range results {
		if len(a.Batch) != 3 {
			t.Fatal("short batch")
		}
		for _, m := range a.Batch {
			if seen[m.Task.TaskID] {
				t.Fatal("duplicate lease")
			}
			seen[m.Task.TaskID] = true
		}
	}
	for err := range errs {
		if !errors.Is(err, ErrEmpty) {
			t.Fatal(err)
		}
	}
}

func TestF1VerificationUsesOnlyReservedCapacity(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	rerun := 100.0
	h.seed(t, 3, &rerun)
	verifier := h.worker(t, 1)
	w := h.workerAs(t, "contributor-2", 1)

	first, err := h.engine.Claim(context.Background(), Thread, verifier.Owner, verifier.TabID)
	if err != nil {
		t.Fatal(err)
	}
	submitF1(t, h.engine, verifier, first)

	verification, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Lease.TaskID != first.Lease.TaskID {
		t.Fatalf("verification lease task = %q, want %q", verification.Lease.TaskID, first.Lease.TaskID)
	}

	ordinary, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Lease.TaskID == verification.Lease.TaskID {
		t.Fatal("ordinary lease reused the verification task")
	}
	if _, err = h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("fourth lease = %v, want capacity bound", err)
	}
}

func TestF1MaxBatchAndRollback(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	j := batchJobF1(t, h, 10000, 10000)
	w := h.worker(t, 1)
	a, err := e.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil || len(a.Batch) != 0 {
		t.Fatalf("max batch: %d %v", len(a.Batch), err)
	}
	got, err := e.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil || got.Progress.Running != 1 {
		t.Fatalf("max progress: %+v %v", got.Progress, err)
	}
	// A signing failure must roll back every member, including the first lease.
	h2 := newHarness(t)
	e2 := h2.engine
	j2 := batchJobF1(t, h2, 3, 3)
	w2 := h2.worker(t, 1)
	e2.TaskKey = nil
	if _, err = e2.Claim(context.Background(), Thread, w2.Owner, w2.TabID); err == nil {
		t.Fatal("invalid signer succeeded")
	}
	got, err = e2.Get(context.Background(), Thread, "developer", j2.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range got.Tasks {
		if task.Lease != nil || task.Attempts != 0 || task.Generation != 0 {
			t.Fatal("failed transaction leaked lease")
		}
	}
}
