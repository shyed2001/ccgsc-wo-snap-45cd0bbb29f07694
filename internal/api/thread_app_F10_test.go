package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestF10ThreadAdminApprovalRequiresRoleAndValidQuota(t *testing.T) {
	handler, authSvc, _ := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authSvc, "A", "admin")
	developer := bearerKey(t, authSvc, "B", "developer")
	send := func(tok, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "http://s/admin/v1/thread-apps/AppAlpha", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if r := send("", `{"owner":"dev-A","maxWorkers":2}`); r.Code != 401 {
		t.Errorf("anonymous approved Thread app: HTTP %d", r.Code)
	}
	if r := send(developer, `{"owner":"dev-A","maxWorkers":2}`); r.Code != 403 {
		t.Errorf("developer self-approved Thread app: HTTP %d", r.Code)
	}
	if r := send(admin, `{"owner":"dev-A","maxWorkers":0}`); r.Code != 400 {
		t.Errorf("zero worker quota approved: HTTP %d", r.Code)
	}
	if r := send(admin, `{"owner":"dev-A","maxWorkers":2}`); r.Code != 200 {
		t.Fatalf("admin thread policy write HTTP %d: %s", r.Code, r.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/thread-apps/AppAlpha?owner=dev-A", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"maxWorkers":2`) {
		t.Errorf("read policy HTTP %d body %s", rec.Code, rec.Body.String())
	}
}
