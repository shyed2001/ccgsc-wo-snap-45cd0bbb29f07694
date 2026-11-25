package api

import (
	"testing"

	"ccgsc/poolscore/internal/pool"
)

func TestResultViewIncludesTrustEvidenceS1(t *testing.T) {
	view := resultView(pool.Result{
		CanaryRan:     true,
		CanaryState:   "pass",
		CanaryOK:      true,
		TrustComplete: true,
		Rerun:         true,
		RerunOK:       true,
		Flagged:       true,
		FlagReason:    "unverified",
	})
	for key, want := range map[string]any{
		"canaryRan": true, "canaryState": "pass", "canaryOk": true,
		"trustComplete": true, "rerun": true, "rerunOk": true,
		"flagged": true, "flagReason": "unverified",
	} {
		if got := view[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}
