package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/threadapp"
)

func TestF12DewHTTPPolicyAndResults(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	raw, key, err := authService.CreateKey(context.Background(), auth.User{ID: "developer-user", AccountStore: "B", Roles: []string{"developer"}}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	devToken, err := authService.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	dev := devToken.AccessToken

	rec := recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/dew", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("default GET = %d %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["windowSeconds"] != float64(pool.DefaultDewReceiptWindowSeconds) || view["windowMinutes"] != float64(10) {
		t.Fatalf("default view = %v", view)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/thread-pool/v1/admin/dew", dev, `{"windowMinutes":15}`); rec.Code != http.StatusForbidden {
		t.Fatalf("developer admin write = %d, want 403", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/thread-pool/dew", admin, `{"windowMinutes":31}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("31 min = %d, want 400", rec.Code)
	}
	rec = recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/dew", admin, `{"windowMinutes":15,"expectedVersion":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save 15 min = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["windowSeconds"] != float64(900) || view["version"] != float64(1) {
		t.Fatalf("saved view = %v", view)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/process-pool/v1/admin/dew", admin, `{"windowSeconds":600,"expectedVersion":9}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale version = %d, want 409", rec.Code)
	}

	if err := threadapp.Approve(context.Background(), engine.Store, key.ID, "dewapp", 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rec = recomputeCall(t, handler, http.MethodPut, "/thread-pool/v1/apps/dewapp/dew", dev, `{"windowMinutes":8,"expectedVersion":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("app override = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["windowSeconds"] != float64(480) || view["owner"] != key.ID {
		t.Fatalf("app view = %v, owner %s", view, key.ID)
	}
	rec = recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/apps/dewapp/dew?owner="+key.ID, admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin app read = %d %s", rec.Code, rec.Body.String())
	}
	if rec := recomputeCall(t, handler, http.MethodGet, "/thread-pool/v1/jobs/missing-job/dew-results", dev, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing job results = %d, want 404", rec.Code)
	}
}
