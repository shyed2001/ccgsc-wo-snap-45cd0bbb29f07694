package api

import (
	"net/http"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

func (s *Server) appEgressRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/ccgsc-jwks.json", func(w http.ResponseWriter, r *http.Request) {
		signer, err := s.Engine.EgressSigner(r.Context())
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=600")
		httpx.JSON(w, 200, signer.Public())
	})
	mux.HandleFunc("GET /pools/egress-apps", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Engine.AppEgressList(r.Context())
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, 200, map[string]any{"apps": v})
	})
	mux.HandleFunc("GET /admin/v1/pools/{pool}/tab-release", s.secure("", "admin", "config:read", s.readTabRelease))
	mux.HandleFunc("PUT /admin/v1/pools/{pool}/tab-release", s.secure("", "admin", "config:write", s.writeTabRelease))
}
func (s *Server) readTabRelease(w http.ResponseWriter, r *http.Request, _ auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	v, err := s.Engine.ReadTabRelease(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, v)
}
func (s *Server) writeTabRelease(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	if !c.HasRole("admin") {
		httpx.Error(w, r, pool.ErrForbidden)
		return
	}
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	q := appegress.DefaultReleasePolicy()
	if !httpx.Decode(w, r, &q) {
		return
	}
	v, err := s.Engine.WriteTabRelease(r.Context(), id, q)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, v)
}
