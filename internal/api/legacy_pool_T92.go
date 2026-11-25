// Path: internal/api/legacy_pool_T92.go | Author: Codex | 2026-11-20
// What/why: read Node pool summaries without Redis access or scheduling changes.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/httpx"
)

const legacyBodyLimit = 1 << 20

type legacyPoolView struct {
	Available          bool             `json:"available"`
	Source             string           `json:"source"`
	FetchedAt          time.Time        `json:"fetchedAt"`
	Workers            []map[string]any `json:"workers"`
	Raw                map[string]any   `json:"raw"`
	Reason             string           `json:"reason,omitempty"`
	DetailsUnavailable string           `json:"detailsUnavailable,omitempty"`
}

func (s *Server) legacyPoolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/legacy-workers", s.secure("", "admin", "metrics:read", s.readLegacyWorkers))
}

func (s *Server) readLegacyWorkers(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	httpx.JSON(w, http.StatusOK, fetchLegacyPool(r.Context(), s.Config.LegacyPoolURL))
}

func fetchLegacyPool(ctx context.Context, origin string) (view legacyPoolView) {
	view = legacyPoolView{Source: "node-pool-admin", Workers: []map[string]any{}, Raw: map[string]any{}}
	defer func() { view.FetchedAt = time.Now().UTC() }()
	if origin == "" {
		view.Reason = "bridge_disabled"
		return
	}
	if config.ValidateLegacyPoolURL(origin) != nil {
		view.Reason = "invalid_configuration"
		return
	}
	u, _ := url.Parse(origin)
	u.Path = "/pool/cluster/summary"
	// Pin localhost to a literal loopback address: no DNS, proxy or redirects.
	if u.Hostname() == "localhost" {
		port := u.Port()
		if port == "" {
			port = "80"
		}
		u.Host = net.JoinHostPort("127.0.0.1", port)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, MaxResponseHeaderBytes: 64 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		view.Reason = "invalid_configuration"
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		view.Reason = "upstream_unavailable"
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		view.Reason = "upstream_http_error"
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, legacyBodyLimit+1))
	if err != nil {
		view.Reason = "upstream_read_error"
		return
	}
	if len(body) > legacyBodyLimit {
		view.Reason = "upstream_body_too_large"
		return
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		view.Reason = "upstream_bad_json"
		return
	}
	var ok, degraded bool
	if json.Unmarshal(payload["ok"], &ok) != nil || !ok {
		view.Reason = "upstream_degraded"
		return
	}
	if raw, exists := payload["degraded"]; exists {
		if json.Unmarshal(raw, &degraded) != nil || degraded {
			view.Reason = "upstream_degraded"
			return
		}
	}
	// Explicit allowlist: upstream errors, internals and unknown fields never leak.
	counts := []string{"totalWorkers", "liveWorkers", "staleWorkers", "clusterCount", "distinctDevices", "liveTabs", "realClusters", "unclusteredLive"}
	raw := map[string]any{}
	if !legacyCounts(payload, raw, counts) {
		view.Reason = "upstream_invalid_counts"
		return
	}
	if b, exists := payload["clusters"]; exists {
		var clusters map[string]map[string]json.RawMessage
		if json.Unmarshal(b, &clusters) != nil || clusters == nil {
			view.Reason = "upstream_invalid_counts"
			return
		}
		projected := map[string]any{}
		for tag, cluster := range clusters {
			if cluster == nil {
				view.Reason = "upstream_invalid_counts"
				return
			}
			values := map[string]any{}
			if !legacyCounts(cluster, values, []string{"total", "live", "stale"}) {
				view.Reason = "upstream_invalid_counts"
				return
			}
			projected[tag] = values
		}
		raw["clusters"] = projected
	}
	// The supplied Node summary has no per-worker records. Support an additive
	// workers array if supplied, but never synthesize identities from cluster counts.
	workers := []map[string]any{}
	if b, exists := payload["workers"]; exists {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(b, &rows) != nil || rows == nil {
			view.Reason = "upstream_invalid_workers"
			return
		}
		for _, row := range rows {
			if row == nil {
				view.Reason = "upstream_invalid_workers"
				return
			}
			worker := map[string]any{}
			for _, key := range []string{"id", "lane", "pool", "lastSeen", "status"} {
				if value, exists := row[key]; exists {
					var text string
					if string(value) == "null" || json.Unmarshal(value, &text) != nil {
						view.Reason = "upstream_invalid_workers"
						return
					}
					worker[key] = text
				}
			}
			workers = append(workers, worker)
		}
	} else {
		view.DetailsUnavailable = "Node summary exposes cluster counts only; worker IDs and last-seen times are not provided."
	}
	view.Available, view.Raw, view.Workers = true, raw, workers
	return
}

func legacyCounts(source map[string]json.RawMessage, dest map[string]any, names []string) bool {
	for _, key := range names {
		if b, exists := source[key]; exists {
			var count int64
			if string(b) == "null" || json.Unmarshal(b, &count) != nil || count < 0 {
				return false
			}
			dest[key] = count
		}
	}
	return true
}
