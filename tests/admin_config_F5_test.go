// Path: tests/admin_config_F5_test.go | Codex | 2026-11-18
// T42 F5: HTTP contracts and authorization; PostgreSQL transactions tested separately.
package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/pg"
)

type configFakeF5 struct {
	versions []pg.ConfigVersion
	actor    string
}

func (f *configFakeF5) AdminAuthor(_ context.Context, name string) (int64, error) {
	f.actor = name
	return 42, nil
}
func (f *configFakeF5) Latest(_ context.Context, page string) (*pg.ConfigVersion, error) {
	if len(f.versions) == 0 {
		return nil, pg.ErrVersionNotFound
	}
	v := f.versions[len(f.versions)-1]
	return &v, nil
}
func (f *configFakeF5) Get(_ context.Context, page string, version int) (*pg.ConfigVersion, error) {
	for _, v := range f.versions {
		if v.Version == version {
			return &v, nil
		}
	}
	return nil, pg.ErrVersionNotFound
}
func (f *configFakeF5) List(_ context.Context, page string, _ int) ([]pg.ConfigVersion, error) {
	return f.versions, nil
}
func (f *configFakeF5) WriteAdminConfig(ctx context.Context, page string, q pg.AdminConfigWrite, author int64, validate func(json.RawMessage) error) (*pg.ConfigVersion, error) {
	if q.Reason == "" {
		return nil, pg.ErrReasonRequired
	}
	if q.ExpectedVersion != nil && *q.ExpectedVersion != len(f.versions) {
		return nil, pg.ErrVersionConflict
	}
	data := q.Settings
	if q.RollbackVersion > 0 {
		v, err := f.Get(ctx, page, q.RollbackVersion)
		if err != nil {
			return nil, err
		}
		data = v.Config
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	v := pg.ConfigVersion{Page: page, Version: len(f.versions) + 1, Config: data, AuthorID: author, Reason: q.Reason, PinnedName: q.PinName}
	f.versions = append(f.versions, v)
	return &v, nil
}
func (f *configFakeF5) Pin(_ context.Context, _ string, version int, name string, _ int64) error {
	if version < 1 || version > len(f.versions) {
		return pg.ErrVersionNotFound
	}
	f.versions[version-1].PinnedName = &name
	return nil
}
func (f *configFakeF5) Unpin(_ context.Context, _ string, version int, _ int64) error {
	if version < 1 || version > len(f.versions) {
		return pg.ErrVersionNotFound
	}
	f.versions[version-1].PinnedName = nil
	return nil
}

func TestF5AdminRoutes(t *testing.T) {
	f := setup(t)
	configs := &configFakeF5{}
	s := &api.Server{Engine: f.engine, Auth: f.auth, Log: f.engine.Log, Configs: configs}
	f.handler = s.Handler()
	_, admin := f.key(t, "admin")
	_, dev := f.key(t, "developer")
	path := "/admin/v1/config/thread-pool-admin"
	f.request(t, "GET", path, "", nil, 401, nil)
	f.request(t, "GET", path, dev, nil, 403, nil)
	f.request(t, "GET", path, admin, nil, 404, nil)
	settings := json.RawMessage(`{"tp.policy":{"adminLimits":{"maxAttempts":{"allowOverride":true,"min":1,"max":4},"rerunPercent":{"allowOverride":true,"min":0,"max":25},"batchSize":{"allowOverride":true,"min":1,"max":4}},"poolDefault":{"maxAttempts":2,"rerunPercent":20,"batchSize":2}}}`)
	q := map[string]any{"settings": settings, "reason": "F5 test", "expectedVersion": 0}
	f.request(t, "POST", path, dev, q, 403, nil)
	created := decode[map[string]any](t, f.request(t, "POST", path, admin, q, 201, nil))
	if created["authorId"] != float64(42) || configs.actor == "" {
		t.Fatal("authenticated actor not resolved")
	}
	f.request(t, "POST", path, admin, q, 409, nil)
	f.request(t, "POST", path, admin, map[string]any{"settings": settings, "reason": "valid", "authorId": 99}, 400, nil)
	f.request(t, "POST", path, admin, map[string]any{"settings": json.RawMessage(`{"tp.policy":{"adminLimits":{"future":{}},"poolDefault":{"future":1}}}`), "reason": "future"}, 501, nil)
	f.request(t, "POST", path+":pin", admin, map[string]any{"version": 1, "name": "F5"}, 200, nil)
	f.request(t, "POST", path+":unpin", admin, map[string]any{"version": 1}, 200, nil)
	f.request(t, "POST", path+":rollback", admin, map[string]any{"rollbackVersion": 1, "reason": "restore"}, 201, nil)
	view := decode[map[string]any](t, f.request(t, "GET", path, admin, nil, 200, nil))
	if view["version"] != float64(2) || len(view["versions"].([]any)) != 2 {
		t.Fatalf("view=%v", view)
	}
	f.request(t, "GET", "/admin/config/thread-pool-admin?version=1", admin, nil, 200, nil)
	f.request(t, "GET", path+"?version=bad", admin, nil, 400, nil)
	cap := decode[map[string]any](t, f.request(t, "GET", "/thread-pool/v1/capabilities", dev, nil, 200, nil))
	limits := cap["serverLimits"].(map[string]any)
	defaults := limits["defaultBatchSize"].(map[string]any)
	rerun := cap["rerun"].(map[string]any)
	if rerun["defaultPercent"] != float64(20) || rerun["maxPercent"] != float64(25) ||
		defaults["cpu"] != float64(2) || defaults["gpu"] != float64(2) ||
		limits["maxBatchSize"] != float64(4) {
		t.Fatalf("capability defaults disagree with policy: %v", cap)
	}
	if cap["adminConfig"].(map[string]any)["available"] != true {
		t.Fatal("configured capability missing")
	}
}

func TestF5UnavailableAndSessionCSRF(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	_, dev := f.key(t, "developer")
	path := "/admin/v1/config/thread-pool-admin"
	f.request(t, "GET", path, admin, nil, 503, nil)
	cap := decode[map[string]any](t, f.request(t, "GET", "/process-pool/v1/capabilities", dev, nil, 200, nil))
	if cap["adminConfig"].(map[string]any)["available"] != false {
		t.Fatal("SQLite falsely advertises PG config")
	}
	// Even a bearer admin cannot bypass CSRF when a session cookie is supplied.
	f.request(t, http.MethodPost, path, admin, map[string]any{"settings": map[string]any{}, "reason": "test"}, 403, map[string]string{"Cookie": "ccgsc_admin_session=invalid"})
	if pool.PolicyPage(pool.Process) != "process-pool-admin" {
		t.Fatal("wrong process page")
	}
}
