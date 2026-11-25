// Path: internal/api/keys_revoke_T99.go
// Author: Copilot
// What/why: T99 admin-only API-key revocation route.
package api

import (
	"net/http"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
)

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	action := strings.TrimSpace(r.PathValue("action"))
	if !strings.HasSuffix(action, ":revoke") {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown key action.")
		return
	}
	keyID := strings.TrimSpace(strings.TrimSuffix(action, ":revoke"))
	if keyID == "" {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-key-id", "keyId is required.")
		return
	}
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	if err := s.Auth.Revoke(r.Context(), keyID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.key.revoked", "keyId", keyID, "revokedBy", actor)
	httpx.JSON(w, http.StatusOK, map[string]any{"keyId": keyID, "revoked": true})
}
