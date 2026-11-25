// Path: internal/api/contributor_counters_F15.go
// What/why: F15 read routes. A contributor reads their own counters. The monitor
// reads the totals. The gate is the same secure() wrapper as the metrics routes.
package api

import (
	"net/http"
	"regexp"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

// contributorInstallID is the shape the contribute page stores for a browser install.
var contributorInstallID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{3,63}$`)

func (s *Server) contributorCounterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /contributor/v1/counters", s.secure("", "contributor", "worker", s.readOwnContributorCounters))
	mux.HandleFunc("GET /admin/v1/contributor-counters", s.secure("", "admin", "metrics:read", s.readContributorCounterTotals))
	mux.HandleFunc("GET /admin/contributor-counters", s.secure("", "admin", "metrics:read", s.readContributorCounterTotals))
}

func (s *Server) readContributorCounterTotals(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	totals, err := s.Engine.ContributorCounters(r.Context(), "")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, totals)
}

func (s *Server) readOwnContributorCounters(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	var key string
	if c.Anonymous || auth.IsAnonymousID(c.Subject) {
		install := strings.TrimSpace(r.Header.Get("X-CCGSC-Install"))
		if !contributorInstallID.MatchString(install) {
			httpx.Problem(w, r, http.StatusNotFound, "not-found", "Contributor counters not found.")
			return
		}
		owns, err := s.Engine.OwnsAnonymousInstall(r.Context(), c.Subject, install)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if !owns {
			httpx.Problem(w, r, http.StatusNotFound, "not-found", "Contributor counters not found.")
			return
		}
		key = pool.ContributorInstallKey(install)
	} else {
		if strings.TrimSpace(c.Subject) == "" {
			httpx.Error(w, r, pool.ErrForbidden)
			return
		}
		key = pool.ContributorAccountKey(c.Subject)
	}
	counters, err := s.Engine.ContributorCounters(r.Context(), key)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, counters)
}
