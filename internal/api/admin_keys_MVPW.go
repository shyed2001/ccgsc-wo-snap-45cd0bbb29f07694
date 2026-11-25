package api

import (
	"encoding/hex"
	"math"
	"net/http"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

func (s *Server) rotateTaskKey(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	if c.User.AccountStore != "A" {
		httpx.Error(w, r, pool.ErrForbidden)
		return
	}
	var q struct {
		OverlapMinutes *int64 `json:"overlapMinutes"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.OverlapMinutes == nil || *q.OverlapMinutes < 0 || *q.OverlapMinutes > math.MaxInt64/int64(time.Minute) {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	key := "task-key-rotation:" + auth.ID()
	event := map[string]any{
		"type": "task_key.rotation", "actor": actor, "overlapMinutes": *q.OverlapMinutes,
		"status": "started", "timestamp": time.Now().UTC(),
	}
	writeAudit := func() error {
		return s.Engine.Store.Update(r.Context(), func(tx store.Tx) error {
			return tx.Put(store.Audit, key, event)
		})
	}
	if err := writeAudit(); err != nil {
		s.Log.Error("task key rotation audit failed", "error", err, "actor", actor)
		httpx.Error(w, r, err)
		return
	}
	if err := s.Engine.RotateTaskKey(r.Context(), time.Duration(*q.OverlapMinutes)*time.Minute); err != nil {
		event["status"] = "failed"
		if auditErr := writeAudit(); auditErr != nil {
			s.Log.Error("task key rotation failure audit failed", "error", auditErr, "actor", actor)
		}
		s.Log.Error("task key rotation failed; key state may have changed", "error", err, "actor", actor)
		httpx.Problem(w, r, 500, "rotation-failed", "Rotation failed; key state may have changed. Inspect the server before retrying.")
		return
	}
	event["status"] = "success"
	if err := writeAudit(); err != nil {
		s.Log.Error("task key rotation completed but audit update failed", "error", err, "actor", actor)
		httpx.Problem(w, r, 500, "rotation-audit-failed", "The key rotated, but its audit update failed. Do not retry without checking the server.")
		return
	}
	s.Log.Info("task key rotated", "actor", actor, "overlapMinutes", *q.OverlapMinutes)
	// T110: the browser runner trusts only PINNED keys. Return the public half of
	// the successor so the operator pins it (runner.js OPERATOR_KEYS, rebuild
	// runner.html, deploy to the run host) before contributors claim new tasks.
	pub := s.Engine.TaskPublicKey()
	httpx.JSON(w, http.StatusOK, map[string]string{
		"status":       "ok",
		"keyId":        verify.KeyID(pub),
		"publicKeyHex": hex.EncodeToString(pub),
		"runnerAction": "pin {keyId, publicKeyHex, scope:'operator'} in web/pages/js/runner.js OPERATOR_KEYS, run node web/pages/js/build-runner-html.mjs, deploy runner.html to the run host",
	})
}
