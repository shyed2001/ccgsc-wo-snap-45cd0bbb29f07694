// Path: internal/worker/worker.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: contributor tab transport; no code execution in server; WWPP-R31.1, R32.4, R37.1.
package worker

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Service struct {
	Engine   *pool.Engine
	Auth     *auth.Service
	ClientIP func(*http.Request) string
}

// contributorView is the worker record of a register or heartbeat reply plus the
// read-only contributor control mode of the pool (W10D row 1, D-W10-11).
// Embedding keeps every existing field at the top level, so a tab of an earlier
// round reads the reply exactly as before; the mode is a display hint, never a
// credential and never a secret.
type contributorView struct {
	Egress *pool.AppEgressGrant `json:"egress,omitempty"`
	pool.Worker
	ContributorControls pool.ContributorControls `json:"contributorControls"`
}

// contributorReply answers Register and Heartbeat with the worker and the current
// mode of that pool. The tab reads it at boot and on every heartbeat, so a
// Director change applies on the next heartbeat without a reload.
func (s Service) contributorReply(w http.ResponseWriter, status int, p string, v pool.Worker) {
	httpx.JSON(w, status, contributorView{
		Worker:              v,
		ContributorControls: s.Engine.PoolSettings(p).ContributorControls,
	})
}

func (s Service) Register(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q struct {
		EgressConsentIDs []string       `json:"egressConsentIds,omitempty"`
		EgressConsent    bool           `json:"egressConsent,omitempty"`
		HostID           string         `json:"hostId"`
		Slots            int            `json:"slots"`
		GPU              bool           `json:"gpu"`
		GPUSlots         int            `json:"gpuSlots"`
		GPUs             []pool.GPUInfo `json:"gpus"`
		// RunnerPin is the pin of the page (runner.html sha256 + task key id, W9 F1).
		RunnerPin string `json:"runnerPin"`
		// BrowserTabID identifies THIS browser tab (sessionStorage, R10). Two tabs
		// of one owner share localStorage's hostId, so without it the second
		// register reused and overwrote the first tab's Worker record.
		BrowserTabID string `json:"browserTabId,omitempty"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	clientIP := ""
	if s.ClientIP != nil {
		clientIP = s.ClientIP(r)
	} else if r.RemoteAddr != "" {
		clientIP = r.RemoteAddr
	}
	v, e := s.Engine.Register(r.Context(), p, c.KeyID, pool.Worker{
		HostID:           q.HostID,
		EgressConsent:    q.EgressConsent,
		EgressConsentIDs: q.EgressConsentIDs,
		Slots:            q.Slots,
		GPU:              q.GPU,
		GPUSlots:         q.GPUSlots,
		GPUs:             q.GPUs,
		Anonymous:        c.Anonymous,
		ClientIP:         clientIP,
		RunnerPin:        q.RunnerPin,
		BrowserTabID:     q.BrowserTabID,
	})
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	s.contributorReply(w, 201, p, v)
}
func (s Service) Heartbeat(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q struct {
		Lease         *pool.Lease `json:"lease"`
		EgressRefresh bool        `json:"egressRefresh,omitempty"`
		// RunnerPin is the pin of the page at lease time (W9 F1).
		RunnerPin string `json:"runnerPin"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	v, e := s.Engine.HeartbeatWithPin(r.Context(), p, c.KeyID, r.PathValue("tabId"), q.RunnerPin, q.Lease)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	if q.EgressRefresh && q.Lease != nil {
		grant, err := s.Engine.IssueAppEgress(r.Context(), p, c.KeyID, v.TabID, *q.Lease)
		if err != nil {
			writeEgressError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, 200, contributorView{Worker: v, ContributorControls: s.Engine.PoolSettings(p).ContributorControls, Egress: grant})
		return
	}
	s.contributorReply(w, 200, p, v)
}
func (s Service) Claim(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	v, e := s.Engine.Claim(r.Context(), p, c.KeyID, r.PathValue("tabId"))
	if errors.Is(e, pool.ErrEmpty) {
		w.WriteHeader(204)
		return
	}
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	if err := s.attachEgress(r, p, c.KeyID, r.PathValue("tabId"), &v); err != nil {
		writeEgressError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, 200, v)
}
func (s Service) Submit(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q pool.Submission
	if !httpx.Decode(w, r, &q) {
		return
	}
	v, e := s.Engine.Submit(r.Context(), p, c.KeyID, r.PathValue("tabId"), q)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 200, v)
}

// Tasks sends signed leases as SSE. Auth is checked before every claim, including revocation.
// Unacknowledged frames expire normally; reconnect never resets a fence.
func (s Service) Tasks(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	if _, e := s.Engine.Heartbeat(r.Context(), p, c.KeyID, r.PathValue("tabId"), nil); e != nil {
		httpx.Error(w, r, e)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		httpx.Problem(w, r, 500, "stream-unavailable", "Streaming unavailable.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	f.Flush()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, e := s.Auth.Validate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); e != nil {
			return
		}
		v, e := s.Engine.Claim(r.Context(), p, c.KeyID, r.PathValue("tabId"))
		if e == nil {
			if err := s.attachEgress(r, p, c.KeyID, r.PathValue("tabId"), &v); err != nil {
				return
			}
			b, _ := json.Marshal(v)
			if _, e = fmt.Fprintf(w, "id: %s.%s.%d\nevent: task\ndata: %s\n\n", v.Lease.JobID, v.Lease.TaskID, v.Lease.Generation, b); e != nil {
				return
			}
			f.Flush()
		} else if !errors.Is(e, pool.ErrEmpty) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func writeEgressError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pool.ErrEgressUnavailable) {
		httpx.Problem(w, r, 503, "app-egress-unavailable", "The developer token service is unavailable.")
		return
	}
	httpx.Error(w, r, err)
}
func (s Service) attachEgress(r *http.Request, p, owner, worker string, a *pool.Assignment) error {
	if len(a.Batch) == 0 {
		grant, err := s.Engine.IssueAppEgress(r.Context(), p, owner, worker, a.Lease)
		a.Egress = grant
		return err
	}
	for i := range a.Batch {
		grant, err := s.Engine.IssueAppEgress(r.Context(), p, owner, worker, a.Batch[i].Lease)
		if err != nil {
			return err
		}
		a.Batch[i].Egress = grant
	}
	a.Egress = a.Batch[0].Egress
	return nil
}
