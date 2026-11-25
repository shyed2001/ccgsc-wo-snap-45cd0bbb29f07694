package api

import (
	"net/http/httptest"
	"testing"

	"ccgsc/poolscore/internal/pool"
)

func TestResultPageUsesCursorAndLimit(t *testing.T) {
	results := []pool.Result{
		{TaskID: "one", Status: "done"},
		{TaskID: "two", Status: "failed"},
		{TaskID: "three", Status: "done"},
	}

	r := httptest.NewRequest("GET", "/results:bulk?limit=1", nil)
	items, more, cursor, err := resultPage(r, results)
	if err != nil || len(items) != 1 || items[0].TaskID != "one" || !more || cursor == "" {
		t.Fatalf("first page = %#v, more=%v, cursor=%q, err=%v", items, more, cursor, err)
	}
	r = httptest.NewRequest("GET", "/results:bulk?cursor="+cursor+"&limit=2&includeFailed=false", nil)
	items, more, _, err = resultPage(r, results)
	if err != nil || len(items) != 1 || items[0].TaskID != "three" || more {
		t.Fatalf("second page = %#v, more=%v, err=%v", items, more, err)
	}
}

func TestResultViewIncludesTrustEvidence(t *testing.T) {
	view := resultView(pool.Result{
		CanaryRan: true, CanaryState: "pass", CanaryOK: true,
		TrustComplete: true, Rerun: true, RerunOK: true,
		Flagged: true, FlagReason: "review",
	})
	for key, want := range map[string]any{
		"canaryRan": true, "canaryState": "pass", "canaryOk": true,
		"trustComplete": true, "rerun": true, "rerunOk": true,
		"flagged": true, "flagReason": "review",
	} {
		if got := view[key]; got != want {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
}
