package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestF9AdminApprovesProcessAppWithExplicitRoleAndQuota(t *testing.T) {
	handler, authSvc, _ := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authSvc, "A", "admin")
	developer := bearerKey(t, authSvc, "B", "developer")
	send := func(tok, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "http://s/admin/v1/process-apps/AppAlpha", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if r := send("", `{"owner":"dev-A","maxTabs":2}`); r.Code != 401 {
		t.Errorf("unauthenticated status=%d", r.Code)
	}
	if r := send(developer, `{"owner":"dev-A","maxTabs":2}`); r.Code != 403 {
		t.Errorf("developer self-approval status=%d", r.Code)
	}
	if r := send(admin, `{"owner":"dev-A","maxTabs":0}`); r.Code != 400 {
		t.Errorf("K=0 status=%d", r.Code)
	}
	if r := send(admin, `{"owner":"dev-A","maxTabs":2}`); r.Code != 200 {
		t.Fatalf("approved status=%d: %s", r.Code, r.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/process-apps/AppAlpha?owner=dev-A", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"maxTabs":2`) {
		t.Errorf("durable approved read status=%d: %s", rec.Code, rec.Body.String())
	}
}
