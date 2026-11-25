// Path: internal/api/admin_recompute_R18_test.go | WO R18-RECOMPUTE-POLICY (G-R17-1..2)
// What/why: the red tests of the admin API of the per-pool recompute policy:
// the busy policy (skip/defer) and the malformed policy (reject/fraud) ride on
// the existing recompute endpoint, are validated, audited with the actor,
// versioned with the conflict rule and durable across a restart.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
)

// TestAdminRecomputePolicyWriteReadAndAudit is the red test of the policy
// write: both policies are saved in one versioned record, the read answers the
// effective values and the allowed lists, and the audit row carries the actor.
func TestAdminRecomputePolicyWriteReadAndAudit(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"busyPolicy":"defer"}`); rec.Code != http.StatusOK {
		t.Fatalf("busy write = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"malformedPolicy":"fraud","expectedVersion":1}`); rec.Code != http.StatusOK {
		t.Fatalf("malformed write = %d: %s", rec.Code, rec.Body.String())
	}
	rec := recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/recompute", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["busyPolicy"] != string(pool.RecomputeBusyDefer) {
		t.Errorf("the busy policy = %v, want defer", view["busyPolicy"])
	}
	if view["malformedPolicy"] != string(pool.RecomputeMalformedFraud) {
		t.Errorf("the malformed policy = %v, want fraud", view["malformedPolicy"])
	}
	if view["version"] != float64(2) {
		t.Errorf("the version = %v, want 2", view["version"])
	}
	if busy, ok := view["busyPolicies"].([]any); !ok || len(busy) != 2 {
		t.Errorf("the busy policies = %v, want the two documented values", view["busyPolicies"])
	}
	if malformed, ok := view["malformedPolicies"].([]any); !ok || len(malformed) != 2 {
		t.Errorf("the malformed policies = %v, want the two documented values", view["malformedPolicies"])
	}
	saved, ok := engine.SavedRecomputeSettings(pool.Thread)
	if !ok || saved.BusyPolicy != pool.RecomputeBusyDefer || saved.MalformedPolicy != pool.RecomputeMalformedFraud {
		t.Fatalf("saved = %+v (found %v), want defer/fraud", saved, ok)
	}
	var found bool
	if err := engine.Store.View(context.Background(), func(r store.Reader) error {
		rows, err := r.List(store.Audit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			raw := string(row.Value)
			if strings.Contains(raw, `"busyPolicy":"defer"`) && strings.Contains(raw, `"malformedPolicy":"fraud"`) {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the audit bucket: %v", err)
	}
	if !found {
		t.Fatal("no audit row carries both policy writes")
	}
}

// TestAdminRecomputePolicyReadAnswersTheDefaults is the red test of the read
// defaults: a pool that never chose keeps skip/reject.
func TestAdminRecomputePolicyReadAnswersTheDefaults(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	rec := recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/recompute", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["busyPolicy"] != string(pool.RecomputeBusySkip) {
		t.Errorf("the default busy policy = %v, want skip", view["busyPolicy"])
	}
	if view["malformedPolicy"] != string(pool.RecomputeMalformedReject) {
		t.Errorf("the default malformed policy = %v, want reject", view["malformedPolicy"])
	}
}

// TestAdminRecomputePolicyValidation is the red test of the validation: an
// unknown policy value or an empty body is a clear error.
func TestAdminRecomputePolicyValidation(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"busyPolicy":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown busy policy = %d, want 400", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"malformedPolicy":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown malformed policy = %d, want 400", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"busyPolicy":"defer","expectedVersion":-1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a negative version = %d, want 400", rec.Code)
	}
}

// TestAdminRecomputePolicyStaleVersionAnswers409 is the red test of the
// conflict rule: a stale expectedVersion answers 409 and writes nothing.
func TestAdminRecomputePolicyStaleVersionAnswers409(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"busyPolicy":"defer"}`); rec.Code != http.StatusOK {
		t.Fatalf("busy write = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"malformedPolicy":"fraud","expectedVersion":0}`); rec.Code != http.StatusConflict {
		t.Errorf("a stale write = %d, want 409", rec.Code)
	}
	if got := engine.RecomputeMalformedPolicy(pool.Thread); got != pool.RecomputeMalformedReject {
		t.Fatalf("the malformed policy = %q, want the refused write to change nothing", got)
	}
}

// TestAdminRecomputePolicySurvivesRestart is the red test of the durability:
// the saved policies are still in force after a restart of the engine.
func TestAdminRecomputePolicySurvivesRestart(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"busyPolicy":"defer","malformedPolicy":"fraud"}`); rec.Code != http.StatusOK {
		t.Fatalf("policy write = %d: %s", rec.Code, rec.Body.String())
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	restarted, err := pool.New(engine.Store, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New after the restart: %v", err)
	}
	if got := restarted.RecomputeBusyPolicy(pool.Thread); got != pool.RecomputeBusyDefer {
		t.Fatalf("the busy policy after the restart = %q, want defer", got)
	}
	if got := restarted.RecomputeMalformedPolicy(pool.Thread); got != pool.RecomputeMalformedFraud {
		t.Fatalf("the malformed policy after the restart = %q, want fraud", got)
	}
}

// TestAdminRecomputePolicyReusesTheSharedRecomputeEndpoint keeps the endpoint
// contract intact: the policy rides the same admin routes of both pools.
func TestAdminRecomputePolicyReusesTheSharedRecomputeEndpoint(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/pp/recompute", admin, `{"busyPolicy":"defer"}`); rec.Code != http.StatusOK {
		t.Errorf("the process pool write = %d: %s", rec.Code, rec.Body.String())
	}
}
