// Path: internal/api/chain.go | Author: Hermes (MiniMax) for the owner | 2026-11-18
// What/why: the HTTP routes of the per-job audit chain and of the global Merkle
// checkpoints, so a developer can export the chain and verify it independently and an
// admin can read the checkpoints; WWPP-R22.3, R26.2, R35.2 (WO T42 F3).
package api

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"net/http"
	"strconv"
)

// jobChain serves the audit chain of one job with the verdict of the verification.
// The job must belong to the caller and to the pool, so the chain of another developer
// is never readable.
func (s *Server) jobChain(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	report, e := s.Engine.JobChain(r.Context(), p, c.KeyID, r.PathValue("jobId"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 200, report)
}

// merkleCheckpoints serves the global Merkle checkpoints with the verdict of the
// verification of every one of them.
func (s *Server) merkleCheckpoints(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 || n > 1000 {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
		limit = n
	}
	report, e := s.Engine.MerkleCheckpoints(r.Context(), limit)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 200, report)
}
