// Path: tests/redis_admin_T91_test.go | Author: Codex | 2026-11-20
// T91: real HTTP handlers with a version-store double; no Redis/PG services.
package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store/pg"
)

func TestT91AdminRoutes(t *testing.T) {
	for _, page := range []string{"thread-pool-admin", "process-pool-admin"} {
		t.Run(page, func(t *testing.T) {
			f := setup(t)
			configs := &configFakeF5{}
			runner := redisadmin.New("", "", "")
			s := &api.Server{Engine: f.engine, Auth: f.auth, Log: f.engine.Log, Configs: configs, RedisAdmin: runner}
			f.handler = s.Handler()
			_, admin := f.key(t, "admin")
			_, dev := f.key(t, "developer")
			statusPath := "/admin/v1/redis/status"
			f.request(t, "GET", statusPath, "", nil, 401, nil)
			f.request(t, "GET", statusPath, dev, nil, 403, nil)
			status := decode[redisadmin.Status](t, f.request(t, "GET", statusPath, admin, nil, 200, nil))
			if status.Enabled || status.Current != nil || status.Message != "applied by ops, not by pools-core" {
				t.Fatalf("status=%+v", status)
			}
			path := "/admin/v1/config/" + page
			for _, raw := range []string{`{"redis.memory.fixed_gb":5}`, `{"redis.memory.dynamic_percent":9}`, `{"redis.memory.interval_min":61}`, `{"redis.memory.mode":"bad"}`, `{"redis.persistence.mode":"off"}`, `{"redis.memory.fixed_gb":"2"}`, `{"redis.memory.fixed_gb":null}`} {
				f.request(t, "POST", path, admin, map[string]any{"settings": json.RawMessage(raw), "reason": "invalid T91"}, 400, nil)
			}
			if len(configs.versions) != 0 {
				t.Fatal("invalid config was persisted")
			}
			c := redisadmin.DefaultConfig()
			c.FixedGB = 4
			c.Persistence = "snapshot"
			f.request(t, "POST", path, dev, map[string]any{"settings": c, "reason": "T91"}, 403, nil)
			f.request(t, "POST", path, admin, map[string]any{"settings": c, "reason": "T91", "expectedVersion": 0}, 201, nil)
			if runner.Config() != c {
				t.Fatal("saved desired config not adopted")
			}
			c.FixedGB = 2
			f.request(t, "POST", path, admin, map[string]any{"settings": c, "reason": "T91 next", "expectedVersion": 1}, 201, nil)
			f.request(t, "POST", path, admin, map[string]any{"settings": c, "reason": "stale", "expectedVersion": 1}, 409, nil)
			f.request(t, "POST", path+":rollback", admin, map[string]any{"rollbackVersion": 1, "reason": "T91 restore", "expectedVersion": 2}, 201, nil)
			if runner.Config().FixedGB != 4 {
				t.Fatal("rollback not adopted")
			}
		})
	}
}

type redisVersionsT91 struct {
	configFakeF5
	pages map[string]*pg.ConfigVersion
	err   error
}

func (f *redisVersionsT91) Latest(_ context.Context, page string) (*pg.ConfigVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v := f.pages[page]; v != nil {
		return v, nil
	}
	return nil, pg.ErrVersionNotFound
}

func TestT91RestoreLatestSharedPolicy(t *testing.T) {
	// An empty address fails locally; startup selection itself needs no network.
	r := redisadmin.New("", "test-user", "test-password")
	store := &redisVersionsT91{pages: map[string]*pg.ConfigVersion{
		"thread-pool-admin":  {Config: json.RawMessage(`{"redis.memory.fixed_gb":2}`), CreatedAt: time.Unix(2, 0)},
		"process-pool-admin": {Config: json.RawMessage(`{"redis.memory.fixed_gb":4}`), CreatedAt: time.Unix(3, 0)},
	}}
	s := &api.Server{Configs: store, RedisAdmin: r}
	if err := s.InitializeRedisAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Config().FixedGB != 4 {
		t.Fatal("newest shared policy was not restored")
	}
	store.err = pg.ErrSchemaNotFound
	if err := s.InitializeRedisAdmin(context.Background()); err == nil {
		t.Fatal("missing schema fell back to defaults")
	}
	if r.Config().FixedGB != 4 {
		t.Fatal("failed load changed desired state")
	}
}
