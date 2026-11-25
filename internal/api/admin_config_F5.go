// Path: internal/api/admin_config_F5.go | Codex | 2026-11-18
// T42 F5: authenticated routes over the existing pool_config_version store.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store/pg"
	"github.com/jackc/pgx/v5"
)

type AdminConfigs interface {
	AdminAuthor(context.Context, string) (int64, error)
	Latest(context.Context, string) (*pg.ConfigVersion, error)
	Get(context.Context, string, int) (*pg.ConfigVersion, error)
	List(context.Context, string, int) ([]pg.ConfigVersion, error)
	WriteAdminConfig(context.Context, string, pg.AdminConfigWrite, int64, func(json.RawMessage) error) (*pg.ConfigVersion, error)
	Pin(context.Context, string, int, string, int64) error
	Unpin(context.Context, string, int, int64) error
}

func (s *Server) configRoutes(mux *http.ServeMux) {
	// T91 / R42.8 (Codex, 2026-11-20): authenticated observation, never capabilities.
	mux.HandleFunc("GET /admin/v1/redis/status", s.secure("", "admin", "config:read", s.readRedisStatus))
	for _, prefix := range []string{"/admin/v1/config/", "/admin/config/"} {
		mux.HandleFunc("GET "+prefix+"{page}", s.secure("", "admin", "config:read", s.readAdminConfig))
		mux.HandleFunc("POST "+prefix+"{page}", s.secure("", "admin", "config:write", s.writeAdminConfig))
	}
}

func (s *Server) configAccess(w http.ResponseWriter, r *http.Request, c auth.Claims, write bool) (int64, bool) {
	if c.User.AccountStore != "A" {
		httpx.Error(w, r, pool.ErrForbidden)
		return 0, false
	}
	if write {
		if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
			session, err := s.Auth.ValidateSession(r.Context(), cookie.Value)
			if err != nil || session.CSRFToken == "" || r.Header.Get("X-CSRF-Token") != session.CSRFToken {
				httpx.Problem(w, r, 403, "invalid-csrf", "A valid session CSRF token is required.")
				return 0, false
			}
		}
	}
	if s.Configs == nil {
		httpx.Problem(w, r, 503, "config-store-unavailable", "Versioned admin settings require PostgreSQL with the pool_config schema.")
		return 0, false
	}
	if !write {
		return 0, true
	}
	author, err := s.Configs.AdminAuthor(r.Context(), c.User.ID)
	if err != nil {
		configError(w, r, err)
		return 0, false
	}
	return author, true
}

func configError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pg.ErrAuthorRequired):
		httpx.Error(w, r, pool.ErrForbidden)
	case errors.Is(err, pg.ErrVersionConflict), errors.Is(err, pg.ErrVersionInUse):
		httpx.Problem(w, r, 409, "config-conflict", "Config version conflict.")
	case errors.Is(err, pg.ErrVersionNotFound), errors.Is(err, pg.ErrPageNotFound), errors.Is(err, pgx.ErrNoRows):
		httpx.Problem(w, r, 404, "config-not-found", "Config page or version not found.")
	case errors.Is(err, pg.ErrInvalidConfig), errors.Is(err, pg.ErrReasonRequired):
		httpx.Problem(w, r, 400, "invalid-config", "Config, reason or version is invalid.")
	case errors.Is(err, pg.ErrSchemaNotFound):
		httpx.Problem(w, r, 503, "config-schema-unavailable", "The config schema is not provisioned.")
	default:
		httpx.Error(w, r, err)
	}
}

func configView(v *pg.ConfigVersion) map[string]any {
	return map[string]any{"page": v.Page, "version": v.Version, "settings": v.Config, "createdAt": v.CreatedAt, "authorId": v.AuthorID, "reason": v.Reason, "pinnedName": v.PinnedName, "inUse": v.InUse, "jsonSchemaVer": v.JSONSchemaVer}
}

func (s *Server) readAdminConfig(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	if _, ok := s.configAccess(w, r, c, false); !ok {
		return
	}
	page := r.PathValue("page")
	var v *pg.ConfigVersion
	var err error
	if raw := r.URL.Query().Get("version"); raw != "" {
		version, e := strconv.Atoi(raw)
		if e != nil || version < 1 {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
		v, err = s.Configs.Get(r.Context(), page, version)
	} else {
		v, err = s.Configs.Latest(r.Context(), page)
	}
	if err != nil {
		configError(w, r, err)
		return
	}
	versions, err := s.Configs.List(r.Context(), page, 100)
	if err != nil {
		configError(w, r, err)
		return
	}
	out := configView(v)
	history := make([]map[string]any, 0, len(versions))
	for i := range versions {
		history = append(history, configView(&versions[i]))
	}
	out["versions"] = history
	httpx.JSON(w, 200, out)
}

func (s *Server) writeAdminConfig(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	author, ok := s.configAccess(w, r, c, true)
	if !ok {
		return
	}
	page, action, _ := strings.Cut(r.PathValue("page"), ":")
	// Both Director pages control one Redis instance. Serialize saves and applies.
	if redisadmin.IsPage(page) {
		s.redisConfigMu.Lock()
		defer s.redisConfigMu.Unlock()
	}
	if action == "pin" || action == "unpin" {
		var q struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
		}
		if !httpx.Decode(w, r, &q) {
			return
		}
		if q.Version < 1 || (action == "unpin" && q.Name != "") {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
		var err error
		if action == "pin" {
			err = s.Configs.Pin(r.Context(), page, q.Version, q.Name, author)
		} else {
			err = s.Configs.Unpin(r.Context(), page, q.Version, author)
		}
		if err != nil {
			configError(w, r, err)
			return
		}
		httpx.JSON(w, 200, map[string]any{"page": page, "version": q.Version, "action": action})
		return
	}
	if action != "" && action != "rollback" {
		httpx.Error(w, r, pool.ErrUnsupported)
		return
	}
	var q pg.AdminConfigWrite
	if !httpx.Decode(w, r, &q) {
		return
	}
	if action == "rollback" && q.RollbackVersion < 1 {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	v, err := s.Configs.WriteAdminConfig(r.Context(), page, q, author, func(data json.RawMessage) error {
		if _, err := pool.DecodePolicyConfig(page, data); err != nil {
			return err
		}
		if err := pool.ValidateGPUAdminConfig(page, data); err != nil {
			return err
		}
		if err := redisadmin.ValidateAdminConfig(page, data); err != nil {
			return pg.ErrInvalidConfig
		}
		return nil
	})
	if err != nil {
		configError(w, r, err)
		return
	}
	gpuSettings := q.Settings
	if q.RollbackVersion > 0 {
		gpuSettings = v.Config
	}
	if gpu, found, gpuErr := pool.DecodeGPUAdminConfig(page, gpuSettings); gpuErr != nil {
		configError(w, r, gpuErr)
		return
	} else if found {
		if gpuErr = s.Engine.SetGPUConfig(gpu); gpuErr != nil {
			configError(w, r, gpuErr)
			return
		}
	}
	out := configView(v)
	if cfg, found, err := redisadmin.DecodeAdminConfig(page, v.Config); err == nil && found && s.RedisAdmin != nil {
		// Saved and applied are separate outcomes: Redis failure must not disguise
		// a committed PG version as a failed save or trigger duplicate retries.
		if err := s.RedisAdmin.Configure(r.Context(), cfg); err != nil {
			s.Log.Warn("admin.redis_apply_failed", "page", page, "error", err)
		}
		out["redis"] = s.RedisAdmin.Status(r.Context())
	}
	httpx.JSON(w, 201, out)
}

func (s *Server) configureAdminStore() {
	if s.Configs == nil {
		if db, ok := s.Engine.Store.(*pg.DB); ok {
			s.Configs = pg.NewConfigStore(db.Pool, s.Log)
		}
	}
}

func (s *Server) policyCapabilities(ctx context.Context, p string) map[string]any {
	out := map[string]any{"available": false, "backend": "pool_config_version", "requiresProvisionedSchema": true, "routes": "/admin/v1/config/{page}", "layers": []string{"adminLimits", "poolDefault", "jobOverride"}, "fields": []string{"policy", "needs", "priority", "maxAttempts", "batchSize", "threshold", "rerunPercent"}}
	if s.Configs == nil {
		return out
	}
	v, err := s.Configs.Latest(ctx, pool.PolicyPage(p))
	if errors.Is(err, pg.ErrVersionNotFound) {
		out["available"] = true
		out["configVersion"] = "builtin-v1"
		return out
	}
	if err != nil {
		out["status"] = "config-store-unavailable"
		return out
	}
	policy, err := pool.DecodePolicyConfig(v.Page, v.Config)
	if err != nil {
		out["status"] = "invalid-policy"
		return out
	}
	out["available"] = true
	out["configVersion"] = v.Page + ":" + strconv.Itoa(v.Version)
	out["policy"] = policy
	return out
}

// Keep existing capability fields consistent with the detailed F5 policy block.
func (s *Server) applyPolicyCapabilities(ctx context.Context, p string, caps map[string]any) map[string]any {
	current := s.policyCapabilities(ctx, p)
	caps["adminConfig"] = current
	policy, ok := current["policy"].(*pool.PolicyConfig)
	if !ok || policy == nil {
		return caps
	}
	for field, rule := range policy.AdminLimits {
		var number float64
		_ = json.Unmarshal(policy.PoolDefault[field], &number)
		switch field {
		case "rerunPercent":
			rerun := caps["rerun"].(map[string]any)
			rerun["defaultPercent"] = number
			if *rule.Max < rerun["maxPercent"].(float64) {
				rerun["maxPercent"] = *rule.Max
			}
			rerun["minPercent"] = *rule.Min
			rerun["allowOverride"] = rule.AllowOverride
		case "batchSize":
			defaults := caps["serverLimits"].(map[string]any)["defaultBatchSize"].(map[string]int)
			defaults["cpu"] = int(number)
			defaults["gpu"] = int(number)
			caps["serverLimits"].(map[string]any)["maxBatchSize"] = int(*rule.Max)
		case "maxAttempts":
			// maxAttempts is a job policy, not a capability field.
		case "priority":
			caps["priorityClasses"] = rule.Allowed
			quotas := caps["quotas"].(map[string]any)
			quotas["priorityHighAllowed"] = quotas["priorityHighAllowed"].(bool) && containsPolicyChoice(rule.Allowed, "high")
		case "needs":
			caps["gpu"].(map[string]any)["supported"] = containsPolicyChoice(rule.Allowed, "gpu") || containsPolicyChoice(rule.Allowed, "gpu-preferred")
		}
	}
	return caps
}

func containsPolicyChoice(choices []string, value string) bool {
	for _, choice := range choices {
		if choice == value {
			return true
		}
	}
	return false
}
