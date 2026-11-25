// Path: internal/api/denylist_MVPW.go | Author: cmdc for Claude Opus 5.5 | 2026-11-19
// What/why: admin routes over the persisted token deny-list (MVPW slice A).
package api

import (
	"net/http"
	"strings"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
)

// denyRoutes registers the deny-list administration pair.
//
// The action is a colon suffix on the last segment, matching the job :cancel and
// session :close convention: ServeMux cannot attach :revoke to a wildcard, so the
// handler validates the complete segment.
func (s *Server) denyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/v1/tokens:revoke", s.secure("", "admin", "keys:write", s.revokeToken))
	// W7 fix: an anonymous subject has no key row to revoke, and its jti changes
	// on every renewal, so the only durable administrative switch is the subject.
	mux.HandleFunc("POST /admin/v1/subjects:revoke", s.secure("", "admin", "keys:write", s.revokeSubject))
	mux.HandleFunc("GET /admin/v1/tokens/revoked", s.secure("", "admin", "keys:write", s.revokedTokens))
}

// revokeToken denies one token id until that token's own expiry.
//
// expiresAt is optional. When the caller omits it the entry lives for the maximum
// token lifetime, so an operator who did not measure the token never creates a
// permanent entry by accident.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	var q struct {
		TokenID   string `json:"tokenId"`
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expiresAt,omitempty"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	tokenID := strings.TrimSpace(q.TokenID)
	if tokenID == "" {
		httpx.Problem(w, r, 400, "invalid-token-id", "tokenId is required.")
		return
	}
	expiresAt, ok := parseDenyExpiry(w, r, q.ExpiresAt)
	if !ok {
		return
	}
	// A cookie session mints synthetic claims with empty ID and KeyID, so record
	// the actor from Subject when User.ID is absent (same idiom as review routes).
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	entry, err := s.Auth.DenyToken(r.Context(), tokenID, strings.TrimSpace(q.Reason), actor, expiresAt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.token.revoked", "tokenId", entry.TokenID, "revokedBy", entry.RevokedBy, "expiresAt", entry.ExpiresAt.Format(time.RFC3339))
	httpx.JSON(w, 201, denyEntryView(entry.TokenID, entry.Reason, entry.RevokedBy, entry.RevokedAt, entry.ExpiresAt))
}

// revokeSubject denies every token of one anonymous subject until expiresAt.
//
// W7 fix: the anonymous contributor token has no key row, and its jti is minted
// fresh on each renewal, so revoking a single jti only kills the presented bearer
// while the tab renews into a new one. This route denies the subject itself (the
// token kid, which every renewal keeps), which is the switch an operator needs.
// The presented jti is denied with it so the visible bearer stops at once too.
// expiresAt is optional, exactly like POST /admin/v1/tokens:revoke.
func (s *Server) revokeSubject(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	var q struct {
		Subject   string `json:"subject"`
		TokenID   string `json:"tokenId,omitempty"`
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expiresAt,omitempty"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	subject := strings.TrimSpace(q.Subject)
	if subject == "" {
		httpx.Problem(w, r, 400, "invalid-subject", "subject is required.")
		return
	}
	if !auth.IsAnonymousID(subject) {
		httpx.Problem(w, r, 400, "invalid-subject", "subject must be an anonymous identity (anon- prefix).")
		return
	}
	expiresAt, ok := parseDenyExpiry(w, r, q.ExpiresAt)
	if !ok {
		return
	}
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	entries, err := s.Auth.DenySubjectAndToken(r.Context(), subject, strings.TrimSpace(q.TokenID), strings.TrimSpace(q.Reason), actor, expiresAt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.subject.revoked", "subject", subject, "revokedBy", actor, "entries", len(entries))
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, denyEntryView(entry.TokenID, entry.Reason, entry.RevokedBy, entry.RevokedAt, entry.ExpiresAt))
	}
	httpx.JSON(w, 201, map[string]any{"subject": subject, "items": items})
}

// revokedTokens lists the deny-list, with expiry pending by default.
func (s *Server) revokedTokens(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	includeExpired := r.URL.Query().Get("includeExpired") == "true"
	entries, err := s.Auth.RevokedTokens(r.Context(), includeExpired)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, v := range entries {
		view := denyEntryView(v.TokenID, v.Reason, v.RevokedBy, v.RevokedAt, v.ExpiresAt)
		view["expired"] = v.Expired
		items = append(items, view)
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "includeExpired": includeExpired})
}

// parseDenyExpiry accepts RFC 3339 or empty; obsolete forms are rejected rather
// than guessed, so a client cannot smuggle a timezone-less value into a deny TTL.
func parseDenyExpiry(w http.ResponseWriter, r *http.Request, raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpx.Problem(w, r, 400, "invalid-expires-at", "expiresAt must be an RFC 3339 timestamp.")
		return time.Time{}, false
	}
	if !parsed.After(time.Now()) {
		httpx.Problem(w, r, 400, "expired-expires-at", "expiresAt must be in the future.")
		return time.Time{}, false
	}
	if parsed.After(time.Now().Add(15 * time.Minute)) {
		httpx.Problem(w, r, 400, "expires-at-too-far", "expiresAt cannot exceed the maximum token lifetime.")
		return time.Time{}, false
	}
	return parsed, true
}

func denyEntryView(tokenID, reason, revokedBy string, revokedAt, expiresAt time.Time) map[string]any {
	view := map[string]any{
		"tokenId":   tokenID,
		"revokedAt": revokedAt.UTC().Format(time.RFC3339),
	}
	if !expiresAt.IsZero() {
		view["expiresAt"] = expiresAt.UTC().Format(time.RFC3339)
	}
	if reason != "" {
		view["reason"] = reason
	}
	if revokedBy != "" {
		view["revokedBy"] = revokedBy
	}
	return view
}
