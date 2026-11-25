// Path: internal/pool/anonymous_T96.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-20
// What/why: the low trust rules of an anonymous contributor tab (WO_T96). A volunteer
// tab joins without a login, so the server treats it as low trust: the tab starts at
// the lowest standing that still lets it work, and its result is only fully trusted
// when the server side canary answered the task. The live tab caps of the anonymous
// join live here too, because the count of live tabs is a property of the engine.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrAnonymousQuota is returned when an anonymous tab would exceed a live tab cap.
//
// The caller turns it into a 429 problem: the request is refused, not queued.
var ErrAnonymousQuota = errors.New("anonymous contributor quota exceeded")

// AnonymousLimits bounds the live anonymous tabs of the pool.
type AnonymousLimits struct {
	// MaxLiveTabsPerIP is the number of live anonymous tabs one client IP may hold.
	MaxLiveTabsPerIP int
	// MaxTotalTabs is the number of live anonymous tabs the engine holds in total.
	MaxTotalTabs int
}

// DefaultAnonymousLimits returns the defaults of WO_T96.
func DefaultAnonymousLimits() AnonymousLimits {
	return AnonymousLimits{MaxLiveTabsPerIP: 8, MaxTotalTabs: 500}
}

// SetAnonymousLimits applies the live tab caps of the anonymous join.
func (e *Engine) SetAnonymousLimits(l AnonymousLimits) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.anonymousLimits = l
}

// AnonymousLimits returns the live tab caps of the anonymous join.
func (e *Engine) AnonymousLimits() AnonymousLimits {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.anonymousLimits
}

// liveAnonymousTab reports whether a stored worker record is a live anonymous tab.
//
// A tab is live while its heartbeat is inside the same freshness window the claim
// path uses, so a tab that went away frees its slot again.
func (e *Engine) liveAnonymousTab(w Worker, now time.Time) bool {
	// T114: a stale-runner tab computes nothing, so it does not hold a live slot;
	// otherwise old tabs that were never reloaded lock out fresh ones.
	return w.Anonymous && !w.RunnerStale && now.Sub(w.Heartbeat) <= 2*e.TTL
}

// liveTab reports whether a stored worker record of a stored-key subject is live:
// the same freshness window as a live anonymous tab, without the stale-runner rule
// (a stored key is validated on every call, so its runner cannot silently rot).
func (e *Engine) liveTab(w Worker, now time.Time) bool {
	return now.Sub(w.Heartbeat) <= 2*e.TTL
}

// CheckAnonymousLimits verifies that adding an anonymous tab for the given client IP
// does not exceed MaxTotalTabs or MaxLiveTabsPerIP.
func (e *Engine) CheckAnonymousLimits(ctx context.Context, clientIP string) error {
	return e.Store.View(ctx, func(r store.Reader) error {
		return e.checkAnonymousLimits(r, clientIP)
	})
}

// checkAnonymousLimits is the anonymous live-tab check over an existing view.
// Register runs it inside its store transaction (W9b row 2), so the state the
// check sees is the state the registration writes; calling Store.View from the
// callback would read outside that transaction (and re-enter the single
// connection of the debug store).
func (e *Engine) checkAnonymousLimits(r store.Reader, clientIP string) error {
	return e.checkAnonymousLimitsExcept(r, clientIP, "")
}

// checkAnonymousLimitsExcept is checkAnonymousLimits with one worker record
// excluded from both counts (W9c row 3). A same-HostID re-register revives its
// own tab, so the tab must not count against itself.
func (e *Engine) checkAnonymousLimitsExcept(r store.Reader, clientIP, exceptTabID string) error {
	limits := e.AnonymousLimits()
	now := e.Now()
	rows, err := r.List(store.Workers)
	if err != nil {
		return err
	}
	totalLive := 0
	ipLive := 0
	for _, row := range rows {
		var w Worker
		if err := json.Unmarshal(row.Value, &w); err != nil {
			continue
		}
		if exceptTabID != "" && w.TabID == exceptTabID {
			continue
		}
		if e.liveAnonymousTab(w, now) {
			totalLive++
			if clientIP != "" && w.ClientIP == clientIP {
				ipLive++
			}
		}
	}
	if limits.MaxTotalTabs > 0 && totalLive >= limits.MaxTotalTabs {
		return ErrAnonymousQuota
	}
	if limits.MaxLiveTabsPerIP > 0 && clientIP != "" && ipLive >= limits.MaxLiveTabsPerIP {
		return ErrAnonymousQuota
	}
	return nil
}

// EvictStaleAnonymousTabs releases the slots of one anonymous subject whose tabs
// are no longer live, so its next registration can take a slot (W7 fix: stale
// token at the cap). It reports how many records were removed.
//
// Only stale records are removed: a live tab of that subject is never evicted, so
// a bearer cannot use this path to displace a working tab of the same subject, and
// the caps checked by CheckAnonymousLimits still bound how many tabs exist. The
// record is deleted, never rewritten in place: Register mints a new tab id.
func (e *Engine) EvictStaleAnonymousTabs(ctx context.Context, owner string) (int, error) {
	if owner == "" {
		return 0, ErrInvalid
	}
	now := e.Now()
	evicted := 0
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		deleter, ok := tx.(store.DeleteTx)
		if !ok {
			return store.ErrNotImplemented
		}
		rows, er := tx.List(store.Workers)
		if er != nil {
			return er
		}
		for _, row := range rows {
			var w Worker
			if er = json.Unmarshal(row.Value, &w); er != nil {
				return er
			}
			if w.Owner != owner || !w.Anonymous || e.liveAnonymousTab(w, now) {
				continue
			}
			if er = deleter.Delete(store.Workers, w.TabID); er != nil {
				return er
			}
			evicted++
		}
		return nil
	})
	return evicted, err
}

// CountLiveAnonymousTabs returns the total number of live anonymous tabs in the engine.
func (e *Engine) CountLiveAnonymousTabs(ctx context.Context) (int, error) {
	var count int
	now := e.Now()
	err := e.Store.View(ctx, func(r store.Reader) error {
		rows, err := r.List(store.Workers)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var w Worker
			if err := json.Unmarshal(row.Value, &w); err != nil {
				continue
			}
			if e.liveAnonymousTab(w, now) {
				count++
			}
		}
		return nil
	})
	return count, err
}

func (e *Engine) seedAnonymousReputation(workerID string) {
	if e.Verifier == nil || e.Verifier.Reputation() == nil {
		return
	}
	// W9 F3 (owner decision 2026-11-21): only an unknown tab is seeded. A
	// re-register of a known tab keeps its old score, status and recovery count,
	// so a tab cannot wash its reputation away by registering again.
	if _, err := e.Verifier.Reputation().Worker(workerID); err == nil {
		return
	}
	cfg := e.Verifier.Reputation().Config()
	// Owner 2026-11-21 (T115 option 1): start in the warn band, not on the bar
	// line, so a single lost lease does not bar a new anonymous tab.
	score := anonymousSeedScore(cfg.BarBelow)
	e.Verifier.Reputation().Seed(workerID, score)
}

// anonymousRecoverQuiet is how long a barred (not banned) worker must stay
// quiet before it may lease again (T115 option 3).
const anonymousRecoverQuiet = 15 * time.Minute

// anonymousSeedScore is the start score of an anonymous tab: just above the bar.
func anonymousSeedScore(barBelow float64) float64 {
	if barBelow <= 0 {
		barBelow = 0.5
	}
	return barBelow + 0.05
}
