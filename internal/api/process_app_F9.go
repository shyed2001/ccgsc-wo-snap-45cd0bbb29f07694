package api

// F9: admin-approved logical Process app K-tab quotas; never an anonymous
// route and never a substitute for the existing contributor authentication.
import (
	"net/http"
	"strings"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/processapp"
)

func (s *Server) processAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /admin/v1/process-apps/{appId}", s.secure("", "admin", "config:write", s.approveProcessApp))
	mux.HandleFunc("GET /admin/v1/process-apps/{appId}", s.secure("", "admin", "config:read", s.readProcessApp))
	mux.HandleFunc("POST /process-pool/v1/workers/{tabId}/apps:release", s.secure(pool.Process, "contributor", "worker", s.releaseProcessApp))
}
func (s *Server) approveProcessApp(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	var q struct {
		Egress  *appegress.Policy `json:"egress,omitempty"`
		Owner   string            `json:"owner"`
		MaxTabs int               `json:"maxTabs"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.Egress != nil && !c.HasRole("admin") {
		httpx.Problem(w, r, 403, "forbidden", "Only admins may approve egress policy.")
		return
	}
	q.Owner = strings.TrimSpace(q.Owner)
	appID := r.PathValue("appId")
	if err := processapp.Validate(q.Owner, appID); err != nil {
		httpx.Problem(w, r, 400, "invalid-app-id", "Invalid process application identifier.")
		return
	}
	if q.MaxTabs < 1 || q.MaxTabs > 64 {
		httpx.Problem(w, r, 400, "invalid-tab-quota", "App quota must be between 1 and 64.")
		return
	}
	if err := s.Engine.ApproveAppEgress(r.Context(), "pp", q.Owner, appID, q.MaxTabs, q.Egress); err != nil {
		httpx.Error(w, r, err)
		return
	}
	data, err := s.Engine.GetProcessApp(r.Context(), q.Owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ep, err := s.Engine.AppEgressPolicy(r.Context(), "pp", q.Owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		processapp.Policy
		Egress appegress.Policy `json:"egress"`
	}{data, ep})
}
func (s *Server) readProcessApp(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	appID := r.PathValue("appId")
	if err := processapp.Validate(owner, appID); err != nil {
		httpx.Problem(w, r, 400, "invalid-app-id", "Valid owner and appId required.")
		return
	}
	data, err := s.Engine.GetProcessApp(r.Context(), owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ep, err := s.Engine.AppEgressPolicy(r.Context(), "pp", owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		processapp.Policy
		Egress appegress.Policy `json:"egress"`
	}{data, ep})
}
func (s *Server) releaseProcessApp(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	if err := s.Engine.ReleaseProcessApp(r.Context(), p, c.KeyID, r.PathValue("tabId")); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"released": true})
}
