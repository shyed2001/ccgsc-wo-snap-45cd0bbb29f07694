// Path: internal/api/redis_admin_T91.go | Author: Codex | 2026-11-20
// T91: restore the newest shared Director policy and expose authenticated state.
package api

import (
	"context"
	"errors"
	"net/http"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store/pg"
)

func (s *Server) readRedisStatus(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	if c.User.AccountStore != "A" {
		httpx.Error(w, r, pool.ErrForbidden)
		return
	}
	httpx.JSON(w, 200, s.RedisAdmin.Status(r.Context()))
}

// InitializeRedisAdmin runs after Handler has configured the PG store and before
// serving requests. Latest timestamp wins across the two views of shared Redis.
// A failed store read never falls back to applying defaults over unknown policy.
func (s *Server) InitializeRedisAdmin(ctx context.Context) error {
	if s.RedisAdmin == nil || !s.RedisAdmin.Enabled() {
		return nil
	}
	if s.Configs == nil {
		return errors.New("Redis admin requires the versioned config store")
	}
	var newest *pg.ConfigVersion
	selected := redisadmin.DefaultConfig()
	for _, page := range []string{"thread-pool-admin", "process-pool-admin"} {
		v, err := s.Configs.Latest(ctx, page)
		if errors.Is(err, pg.ErrVersionNotFound) {
			continue
		}
		if err != nil {
			return errors.New("Redis admin config unavailable")
		}
		cfg, found, err := redisadmin.DecodeAdminConfig(page, v.Config)
		if err != nil {
			return err
		}
		if found && (newest == nil || v.CreatedAt.After(newest.CreatedAt)) {
			newest, selected = v, cfg
		}
	}
	// Head fix 2026-11-20 (MiniMax-M3 review T91 #2): with no saved Redis settings on
	// either page, leave the ops-managed Redis policy untouched instead of applying defaults.
	if newest == nil {
		return nil
	}
	// Application failures remain visible in status and do not stop lease service.
	if err := s.RedisAdmin.Configure(ctx, selected); err != nil && s.Log != nil {
		// Startup must not fail on a Redis apply error (see above); without this
		// line the only trace is a status field nobody reads at boot.
		s.Log.Warn("redisadmin.startup_apply_failed", "error", err)
	}
	return nil
}
