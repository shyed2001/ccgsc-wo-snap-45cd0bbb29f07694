package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
)

func TestMVPWRotateTaskKeyAuthorizationAndAudit(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	_, developer := f.key(t, "developer")
	path := "/admin/v1/task-keys:rotate"
	body := map[string]any{"overlapMinutes": 5}
	f.request(t, "POST", path, "", body, 401, nil)
	f.request(t, "POST", path, developer, body, 403, nil)
	f.request(t, "POST", path, admin, map[string]any{"overlapMinutes": -1}, 400, nil)
	f.request(t, "POST", path, admin, map[string]any{}, 400, nil)
	session, err := f.auth.CreateSession(context.Background(), &auth.AdminAccount{
		Username: "test-admin", AccountStore: "A", Roles: []string{"admin"},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"overlapMinutes":5}`))
	req.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: session.ID})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: got %d", w.Code)
	}
	oldKey := f.engine.TaskKeyID
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"overlapMinutes":5}`))
	req.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: session.ID})
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorized rotation: got %d: %s", w.Code, w.Body.String())
	}
	// T110: the response carries the successor public key for the runner pin.
	if !strings.Contains(w.Body.String(), `"keyId":"`+f.engine.TaskKeyID+`"`) || !strings.Contains(w.Body.String(), "publicKeyHex") {
		t.Fatalf("rotation response lacks the runner pin entry: %s", w.Body.String())
	}
	if f.engine.TaskKeyID == oldKey {
		t.Fatal("rotation did not change the signing key")
	}
	if _, err := f.engine.Verifier.Keyring().PublicKeyAt(oldKey, time.Now()); err != nil {
		t.Fatalf("previous key was not retained during overlap: %v", err)
	}
	if _, err := f.engine.Verifier.Keyring().PublicKeyAt(oldKey, time.Now().Add(6*time.Minute)); err == nil {
		t.Fatal("previous key remained valid after overlap")
	}
	err = f.db.View(context.Background(), func(r store.Reader) error {
		records, err := r.List(store.Audit)
		if err != nil {
			return err
		}
		for _, record := range records {
			if strings.Contains(string(record.Value), `"task_key.rotation"`) &&
				strings.Contains(string(record.Value), `"test-admin"`) &&
				strings.Contains(string(record.Value), `"success"`) {
				return nil
			}
		}
		t.Fatal("successful rotation was not audited with the actor")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
