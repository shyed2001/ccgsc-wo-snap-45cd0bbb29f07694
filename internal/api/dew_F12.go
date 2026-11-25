// F12 (owner decision 2026-11-24, WO section 16): HTTP for the Dew window and
// the recorded-result ledger. The engine already stores the policy; these
// routes are the admin panel and the developer API.
package api

import (
	"errors"
	"net/http"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

func (s *Server) dewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/pools/{pool}/dew", s.secure("", "admin", "config:read", s.readDewPolicy))
	mux.HandleFunc("PUT /admin/v1/pools/{pool}/dew", s.secure("", "admin", "config:write", s.writeDewPolicy))
	mux.HandleFunc("GET /admin/v1/pools/{pool}/apps/{appId}/dew", s.secure("", "admin", "config:read", s.readDewAppAdmin))
	mux.HandleFunc("PUT /admin/v1/pools/{pool}/apps/{appId}/dew", s.secure("", "admin", "config:write", s.writeDewAppAdmin))
	for _, spec := range []struct{ prefix, pool string }{{"/thread-pool/v1", pool.Thread}, {"/process-pool/v1", pool.Process}} {
		prefix, p := spec.prefix, spec.pool
		mux.HandleFunc("GET "+prefix+"/admin/dew", s.secure(p, "admin", "config:read", s.readDewPolicy))
		mux.HandleFunc("PUT "+prefix+"/admin/dew", s.secure(p, "admin", "config:write", s.writeDewPolicy))
		mux.HandleFunc("GET "+prefix+"/apps/{appId}/dew", s.secure(p, "developer", "jobs:read", s.readDewApp))
		mux.HandleFunc("PUT "+prefix+"/apps/{appId}/dew", s.secure(p, "developer", "jobs:write", s.writeDewApp))
		mux.HandleFunc("GET "+prefix+"/jobs/{jobId}/dew-results", s.secure(p, "developer", "jobs:read", s.listDewResults))
	}
}

func dewPolicyView(p string, v pool.DewPolicy) map[string]any {
	return map[string]any{
		"poolId":         p,
		"windowSeconds":  v.WindowSeconds,
		"windowMinutes":  v.WindowSeconds / 60,
		"minMinutes":     pool.DewMinWindowSeconds / 60,
		"maxMinutes":     pool.DewMaxWindowSeconds / 60,
		"defaultMinutes": pool.DefaultDewReceiptWindowSeconds / 60,
		"version":        v.Version,
	}
}

func dewAppView(v pool.DewAppPolicy, admin pool.DewPolicy) map[string]any {
	return map[string]any{
		"poolId":             v.PoolID,
		"owner":              v.Owner,
		"appId":              v.AppID,
		"windowSeconds":      v.WindowSeconds,
		"windowMinutes":      v.WindowSeconds / 60,
		"adminWindowSeconds": admin.WindowSeconds,
		"adminMaxMinutes":    admin.WindowSeconds / 60,
		"minMinutes":         pool.DewMinWindowSeconds / 60,
		"maxMinutes":         pool.DewMaxWindowSeconds / 60,
		"version":            v.Version,
	}
}

type dewWindowBody struct {
	WindowSeconds   *int64  `json:"windowSeconds"`
	WindowMinutes   *int64  `json:"windowMinutes"`
	ExpectedVersion *uint64 `json:"expectedVersion"`
	Owner           string  `json:"owner"`
}

// dewWindowSeconds accepts minutes or seconds. Both, when present, must name
// the same window. allowZero is the app clear (0 means no override).
func dewWindowSeconds(q dewWindowBody, allowZero bool) (int64, error) {
	if q.WindowMinutes != nil && q.WindowSeconds != nil && *q.WindowMinutes*60 != *q.WindowSeconds {
		return 0, pool.ErrInvalid
	}
	var seconds int64
	switch {
	case q.WindowSeconds != nil:
		seconds = *q.WindowSeconds
	case q.WindowMinutes != nil:
		seconds = *q.WindowMinutes * 60
	default:
		return 0, pool.ErrInvalid
	}
	if seconds == 0 && allowZero {
		return 0, nil
	}
	if seconds < pool.DewMinWindowSeconds || seconds > pool.DewMaxWindowSeconds {
		return 0, pool.ErrInvalid
	}
	return seconds, nil
}

func (s *Server) writeDewError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pool.ErrDewPolicyConflict) {
		httpx.Problem(w, r, http.StatusConflict, "dew-policy-conflict", "Dew policy version conflict.")
		return
	}
	httpx.Error(w, r, err)
}

func (s *Server) readDewPolicy(w http.ResponseWriter, r *http.Request, _ auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	v, err := s.Engine.ReadDewPolicy(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dewPolicyView(id, v))
}

func (s *Server) writeDewPolicy(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	var q dewWindowBody
	if !httpx.Decode(w, r, &q) {
		return
	}
	seconds, err := dewWindowSeconds(q, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	saved, err := s.Engine.WriteDewPolicy(r.Context(), id, seconds, q.ExpectedVersion)
	if err != nil {
		s.writeDewError(w, r, err)
		return
	}
	s.Log.Info("security.dewPolicy.write", "director", c.User.ID, "poolId", id, "windowSeconds", saved.WindowSeconds, "version", saved.Version)
	httpx.JSON(w, http.StatusOK, dewPolicyView(id, saved))
}

func (s *Server) readDewApp(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	appID := strings.TrimSpace(r.PathValue("appId"))
	v, admin, err := s.Engine.ReadDewApp(r.Context(), p, c.KeyID, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dewAppView(v, admin))
}

func (s *Server) writeDewApp(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q dewWindowBody
	if !httpx.Decode(w, r, &q) {
		return
	}
	seconds, err := dewWindowSeconds(q, true)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	saved, err := s.Engine.WriteDewApp(r.Context(), p, c.KeyID, strings.TrimSpace(r.PathValue("appId")), seconds, q.ExpectedVersion)
	if err != nil {
		s.writeDewError(w, r, err)
		return
	}
	admin, adminErr := s.Engine.ReadDewPolicy(r.Context(), p)
	if adminErr != nil {
		httpx.Error(w, r, adminErr)
		return
	}
	httpx.JSON(w, http.StatusOK, dewAppView(saved, admin))
}

func (s *Server) readDewAppAdmin(w http.ResponseWriter, r *http.Request, _ auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	v, admin, err := s.Engine.ReadDewApp(r.Context(), id, owner, strings.TrimSpace(r.PathValue("appId")))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dewAppView(v, admin))
}

func (s *Server) writeDewAppAdmin(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	var q dewWindowBody
	if !httpx.Decode(w, r, &q) {
		return
	}
	owner := strings.TrimSpace(q.Owner)
	if owner == "" {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	seconds, err := dewWindowSeconds(q, true)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	saved, err := s.Engine.WriteDewApp(r.Context(), id, owner, strings.TrimSpace(r.PathValue("appId")), seconds, q.ExpectedVersion)
	if err != nil {
		s.writeDewError(w, r, err)
		return
	}
	s.Log.Info("security.dewApp.write", "director", c.User.ID, "poolId", id, "owner", owner, "appId", saved.AppID, "windowSeconds", saved.WindowSeconds, "version", saved.Version)
	admin, adminErr := s.Engine.ReadDewPolicy(r.Context(), id)
	if adminErr != nil {
		httpx.Error(w, r, adminErr)
		return
	}
	httpx.JSON(w, http.StatusOK, dewAppView(saved, admin))
}

func (s *Server) listDewResults(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	rows, err := s.Engine.ListDewResults(r.Context(), p, c.KeyID, jobID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if rows == nil {
		rows = []pool.DewResultRow{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"jobId": jobID, "results": rows})
}
