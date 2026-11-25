// Path: internal/pool/event_sequence_seed_R17_test.go | Author: CommandCode for the owner | 2026-11-23
// What/why: R18B review rows 6/17 - the fallback event counter was renamed from
// "event_sequence" to "pool_event_seq" without a migration. A store that was upgraded
// still holds events of the old counter but no row under the new name; the counter
// restarting at 1 would overwrite an existing event record and sort every new event
// before the stored ones, so the replay would skip them. The counter is now seeded from
// the maximum stored event id.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"fmt"
	"testing"
	"time"
)

// TestR17EventCounterSeedsFromStoredEvents pins the upgrade path of the fallback (SQLite)
// event counter: a store that holds events of the old counter and no row under the new
// name hands out ids strictly after the stored ones and never overwrites a record.
func TestR17EventCounterSeedsFromStoredEvents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const storedMax = 42
	first := fmt.Sprintf("%020d", 1)
	if err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		for _, id := range []string{first, fmt.Sprintf("%020d", storedMax)} {
			ev := Event{ID: id, Type: "task.done", JobID: "legacy", At: time.Now().UTC()}
			if err := tx.Put(store.Events, id, ev); err != nil {
				return err
			}
		}
		// The pre-upgrade state: the counter only exists under the old name.
		return tx.Put(store.Metrics, "event_sequence", storedMax)
	}); err != nil {
		t.Fatalf("prepare the upgraded store: %v", err)
	}
	var next uint64
	if err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		var err error
		next, err = nextEventSequence(tx, eventSequenceName)
		if err != nil {
			return err
		}
		// The stored record with id 1 must survive the new event write untouched.
		var ev Event
		if err := tx.Get(store.Events, first, &ev); err != nil {
			return err
		}
		if ev.JobID != "legacy" {
			return fmt.Errorf("the stored event %s was overwritten: jobId %q", first, ev.JobID)
		}
		return nil
	}); err != nil {
		t.Fatalf("nextEventSequence over the upgraded store: %v", err)
	}
	if next != storedMax+1 {
		t.Fatalf("next event id = %d, want %d (the renamed counter restarted below the stored events)", next, storedMax+1)
	}
}
