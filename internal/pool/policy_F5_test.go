// Path: internal/pool/policy_F5_test.go | Codex | 2026-11-18
// T42 F5: policy precedence, invalid controls, snapshot persistence and replay.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ccgsc/poolscore/internal/store"
)

const policyFixtureF5 = `{"tp.policy":{"adminLimits":{"policy":{"allowOverride":true,"allowed":["partial","fail-fast"]},"maxAttempts":{"allowOverride":true,"min":1,"max":5},"needs":{"allowOverride":false,"allowed":["cpu"]},"threshold":{"allowOverride":true,"min":0,"max":1}},"poolDefault":{"policy":"fail-fast","maxAttempts":2,"needs":"cpu","threshold":0.4}}}`

func putPolicyF5(t *testing.T, e *Engine, version int, data string) {
	t.Helper()
	if err := e.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.ConfigVersions, "active:thread-pool-admin", policyVersion{Page: "thread-pool-admin", Version: version, Config: json.RawMessage(data)})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestF5PolicyPrecedenceAndSnapshot(t *testing.T) {
	h := newHarness(t)
	e := h.engine
	ctx := context.Background()
	seed := h.seed(t, 1, nil)
	putPolicyF5(t, e, 1, policyFixtureF5)
	q := CreateRequest{ModuleArtifactIDs: seed.Request.ModuleArtifactIDs, Tasks: []TaskSpec{{TaskID: "f5"}}}
	j, err := e.Create(ctx, Thread, "dev", "stable", q)
	if err != nil {
		t.Fatal(err)
	}
	if j.Policy != "fail-fast" || j.MaxAttempts != 2 || j.ConfigVersion != "thread-pool-admin:1" || j.Request.Threshold != 0.4 || j.ChosenPolicy.ChosenBy["policy"] != "pool-default" {
		t.Fatalf("job=%+v", j)
	}
	putPolicyF5(t, e, 2, `{"tp.policy":{"adminLimits":{"policy":{"allowOverride":true,"allowed":["partial"]}},"poolDefault":{"policy":"partial"}}}`)
	replay, err := e.Create(ctx, Thread, "dev", "stable", q)
	if err != nil || replay.JobID != j.JobID || replay.ConfigVersion != j.ConfigVersion || replay.Policy != j.Policy {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	next, err := e.Create(ctx, Thread, "dev", "next", q)
	if err != nil || next.Policy != "partial" || next.ConfigVersion != "thread-pool-admin:2" {
		t.Fatalf("new job=%+v err=%v", next, err)
	}
	persisted, err := e.Get(ctx, Thread, "dev", j.JobID)
	if err != nil || persisted.Policy != j.Policy || persisted.ChosenPolicy.ChosenBy["policy"] != "pool-default" {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	pp, err := e.Create(ctx, Process, "dev", "", q)
	if err != nil || pp.ConfigVersion != "builtin-v1" {
		t.Fatalf("pool isolation: %+v %v", pp, err)
	}
}

func TestF5PolicyOverridesAndRejections(t *testing.T) {
	h := newHarness(t)
	seed := h.seed(t, 1, nil)
	putPolicyF5(t, h.engine, 1, policyFixtureF5)
	for _, tc := range []struct {
		name, extra string
		want        error
	}{
		{"valid", `,"policy":"partial","maxAttempts":5`, nil},
		{"ceiling", `,"maxAttempts":6`, ErrInvalid},
		{"floor", `,"maxAttempts":0`, ErrInvalid},
		{"disabled", `,"needs":"cpu"`, ErrForbidden},
		{"disallowed", `,"policy":"threshold"`, ErrInvalid},
		{"zero", `,"threshold":0`, nil},
		{"unsupported", `,"options":{"future":true}`, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"moduleArtifactIds":["` + seed.Request.ModuleArtifactIDs[0] + `"],"tasks":[{"taskId":"t"}]` + tc.extra + `}`
			var q CreateRequest
			if err := json.Unmarshal([]byte(raw), &q); err != nil {
				t.Fatal(err)
			}
			j, err := h.engine.Create(context.Background(), Thread, "dev", "", q)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if err == nil && tc.name == "valid" && (j.Policy != "partial" || j.MaxAttempts != 5 || j.ChosenPolicy.ChosenBy["policy"] != "developer:dev") {
				t.Fatalf("override %+v", j)
			}
			if err == nil && tc.name == "zero" && j.Request.Threshold != 0 {
				t.Fatal("explicit zero lost")
			}
		})
	}
}

func TestF5InvalidPolicyFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"tp.policy":null}`,
		`{"tp.policy":{"adminLimits":{},"poolDefault":{}}}`,
		`{"tp.policy":{"adminLimits":{"unknown":{}},"poolDefault":{"unknown":1}}}`,
		`{"tp.policy":{"adminLimits":{"maxAttempts":{"min":5,"max":2}},"poolDefault":{"maxAttempts":3}}}`,
		`{"tp.policy":{"adminLimits":{"maxAttempts":{"min":1,"max":11}},"poolDefault":{"maxAttempts":3}}}`,
		`{"tp.policy":{"adminLimits":{"policy":{"allowed":["partial","unknown"]}},"poolDefault":{"policy":"partial"}}}`,
		`{"tp.policy":{"adminLimits":{"threshold":{"min":0,"max":1}},"poolDefault":{"threshold":null}}}`,
		`{"tp.policy":{"adminLimits":{"threshold":{"min":0,"max":1}},"poolDefault":{"threshold":0},"ignored":1}}`,
	} {
		if _, err := DecodePolicyConfig("thread-pool-admin", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var q CreateRequest
	if err := json.Unmarshal([]byte(`{"unknown":true}`), &q); err == nil {
		t.Fatal("unknown request field ignored")
	}
	if err := json.Unmarshal([]byte(`{"threshold":null}`), &q); err == nil {
		t.Fatal("null threshold ignored")
	}
}

func TestF5ExplicitZeroIdempotencyAndVerifierLimit(t *testing.T) {
	h := newHarness(t)
	seed := h.seed(t, 1, nil)
	putPolicyF5(t, h.engine, 1, policyFixtureF5)
	q := CreateRequest{ModuleArtifactIDs: seed.Request.ModuleArtifactIDs, Tasks: []TaskSpec{{TaskID: "t"}}}
	if _, err := h.engine.Create(context.Background(), Thread, "dev", "zero", q); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(q)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["threshold"] = json.RawMessage("0")
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(context.Background(), Thread, "dev", "zero", q); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request replay=%v", err)
	}
	putPolicyF5(t, h.engine, 2, `{"tp.policy":{"adminLimits":{"rerunPercent":{"allowOverride":true,"min":0,"max":100}},"poolDefault":{"rerunPercent":20}}}`)
	cfg := h.engine.TrustConfig()
	cfg.RerunMaxPercent = 10
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.Create(context.Background(), Thread, "dev", "", q); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("silent verifier clamp=%v", err)
	}
}
