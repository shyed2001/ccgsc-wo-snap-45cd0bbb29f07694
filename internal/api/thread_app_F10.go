package api

// F10 Thread apps are approved independently from F9 Process apps.
// All mutations are admin-authorization gated, no public self-approval.
import (
	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/threadapp"
	"net/http"
	"strings"
)

func (s *Server) threadAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /admin/v1/thread-apps/{appId}", s.secure("", "admin", "config:write", s.approveThreadApp))
	mux.HandleFunc("GET /admin/v1/thread-apps/{appId}", s.secure("", "admin", "config:read", s.readThreadApp))
}
func (s *Server) approveThreadApp(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	var q struct {
		Egress     *appegress.Policy `json:"egress,omitempty"`
		Owner      string            `json:"owner"`
		MaxWorkers int               `json:"maxWorkers"`
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
	if err := threadapp.Validate(q.Owner, appID); err != nil {
		httpx.Problem(w, r, 400, "invalid-thread-app", "Invalid Thread application identifier.")
		return
	}
	if q.MaxWorkers < 1 || q.MaxWorkers > 64 {
		httpx.Problem(w, r, 400, "invalid-thread-worker-quota", "Quota must be 1..64 Web Worker slots.")
		return
	}
	if err := s.Engine.ApproveAppEgress(r.Context(), "tp", q.Owner, appID, q.MaxWorkers, q.Egress); err != nil {
		httpx.Error(w, r, err)
		return
	}
	policy, err := s.Engine.GetThreadApp(r.Context(), q.Owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ep, err := s.Engine.AppEgressPolicy(r.Context(), "tp", q.Owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		threadapp.Policy
		Egress appegress.Policy `json:"egress"`
	}{policy, ep})
}
func (s *Server) readThreadApp(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	appID := r.PathValue("appId")
	if err := threadapp.Validate(owner, appID); err != nil {
		httpx.Problem(w, r, 400, "invalid-thread-app", "Valid developer key owner and appId required.")
		return
	}
	policy, err := s.Engine.GetThreadApp(r.Context(), owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ep, err := s.Engine.AppEgressPolicy(r.Context(), "tp", owner, appID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		threadapp.Policy
		Egress appegress.Policy `json:"egress"`
	}{policy, ep})
}
