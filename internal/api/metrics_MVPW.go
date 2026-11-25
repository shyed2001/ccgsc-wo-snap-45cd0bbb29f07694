// Path: internal/api/metrics_MVPW.go
// What/why: Slice B metrics aggregation, webhook outbox metrics, GET /admin/v1/metrics, and GET /metrics.
package api

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type metricAggregate struct {
	Name   string    `json:"name"`
	Unit   string    `json:"unit"`
	Source string    `json:"source"`
	Count  int       `json:"count"`
	Sum    float64   `json:"sum"`
	Avg    float64   `json:"avg"`
	Min    float64   `json:"min"`
	Max    float64   `json:"max"`
	Last   float64   `json:"last"`
	LastTS time.Time `json:"lastTs"`
}

func (s *Server) metricsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/metrics", s.secure("", "admin", "metrics:read", s.readAdminMetrics))
	mux.HandleFunc("GET /admin/metrics", s.secure("", "admin", "metrics:read", s.readAdminMetrics))
	mux.HandleFunc("GET /metrics", s.servePrometheusMetrics)
	s.contributorCounterRoutes(mux)
}

func (s *Server) recordWebhookOutboxMetric(tx store.Tx, delivery webhookDelivery) error {
	if s == nil || tx == nil {
		return nil
	}
	var name string
	switch {
	case delivery.Delivered:
		name = "webhook_outbox_delivered"
	case delivery.Status == "failed":
		name = "webhook_outbox_failed"
	default:
		return nil
	}
	ts := time.Now().UTC()
	if s.Engine != nil && s.Engine.Now != nil {
		ts = s.Engine.Now().UTC()
	}
	return store.RecordMetricSample(tx, store.MetricSample{
		TS:     ts,
		Scope:  delivery.Job.PoolID,
		JobID:  delivery.Job.JobID,
		Name:   name,
		Value:  1,
		Unit:   "count",
		Source: "webhook",
	})
}

func (s *Server) metricsNow() time.Time {
	if s != nil && s.Engine != nil && s.Engine.Now != nil {
		return s.Engine.Now().UTC()
	}
	return time.Now().UTC()
}

func parseMetricSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
		return now.UTC().Add(-d), nil
	}
	if iv, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if iv >= 1_000_000_000_000 || iv <= -1_000_000_000_000 {
			return time.UnixMilli(iv).UTC(), nil
		}
		return time.Unix(iv, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid since timestamp or duration %q", raw)
}

func (s *Server) metricFilterFromRequest(r *http.Request) (store.MetricFilter, error) {
	q := r.URL.Query()
	since, err := parseMetricSince(q.Get("since"), s.metricsNow())
	if err != nil {
		return store.MetricFilter{}, err
	}
	jobID := strings.TrimSpace(q.Get("job"))
	if jobID == "" {
		jobID = strings.TrimSpace(q.Get("jobId"))
	}
	return store.MetricFilter{
		Scope: strings.TrimSpace(q.Get("scope")),
		JobID: jobID,
		Since: since,
	}, nil
}

func (s *Server) readAdminMetrics(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	filter, err := s.metricFilterFromRequest(r)
	if err != nil {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-metric-filter", err.Error())
		return
	}
	samples, err := store.QueryMetricSamples(r.Context(), s.Engine.Store, filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if samples == nil {
		samples = []store.MetricSample{}
	}
	aggregates := make(map[string]metricAggregate)
	for _, sample := range samples {
		cur, exists := aggregates[sample.Name]
		if !exists {
			aggregates[sample.Name] = metricAggregate{
				Name:   sample.Name,
				Unit:   sample.Unit,
				Source: sample.Source,
				Count:  1,
				Sum:    sample.Value,
				Avg:    sample.Value,
				Min:    sample.Value,
				Max:    sample.Value,
				Last:   sample.Value,
				LastTS: sample.TS.UTC(),
			}
			continue
		}
		cur.Count++
		cur.Sum += sample.Value
		cur.Avg = cur.Sum / float64(cur.Count)
		if sample.Value < cur.Min {
			cur.Min = sample.Value
		}
		if sample.Value > cur.Max {
			cur.Max = sample.Value
		}
		if !sample.TS.Before(cur.LastTS) {
			cur.Last = sample.Value
			cur.LastTS = sample.TS.UTC()
		}
		aggregates[sample.Name] = cur
	}
	counters := make(map[string]float64, len(aggregates))
	for name, agg := range aggregates {
		if agg.Unit == "count" {
			counters[name] = agg.Sum
		} else {
			counters[name] = agg.Avg
		}
	}
	sinceOut := ""
	if !filter.Since.IsZero() {
		sinceOut = filter.Since.UTC().Format(time.RFC3339Nano)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"scope":    filter.Scope,
		"jobId":    filter.JobID,
		"since":    sinceOut,
		"samples":  samples,
		"metrics":  aggregates,
		"counters": counters,
	})
}

func (s *Server) servePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorizePrometheus(w, r) {
		return
	}
	filter, err := s.metricFilterFromRequest(r)
	if err != nil {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-metric-filter", err.Error())
		return
	}
	samples, err := store.QueryMetricSamples(r.Context(), s.Engine.Store, filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	body := formatPrometheusText(samples)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) authorizePrometheus(w http.ResponseWriter, r *http.Request) bool {
	if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
		if sess, err := s.Auth.ValidateSession(r.Context(), cookie.Value); err == nil {
			if slices.Contains(sess.Roles, "admin") || slices.Contains(sess.Roles, "director") {
				return true
			}
			httpx.Error(w, r, pool.ErrForbidden)
			return false
		}
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Error(w, r, auth.ErrUnauthorized)
			return false
		}
		claims, err := s.Auth.Validate(r.Context(), strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			httpx.Error(w, r, err)
			return false
		}
		if !allowsRoute(claims, "admin", "metrics:read") {
			httpx.Error(w, r, pool.ErrForbidden)
			return false
		}
		return true
	}
	if s.Config.MetricsLoopbackAnonymous && isDirectLoopbackRequest(r) &&
		r.Header.Get("X-Forwarded-For") == "" &&
		r.Header.Get("Forwarded") == "" &&
		r.Header.Get("X-Real-IP") == "" {
		return true
	}
	httpx.Error(w, r, auth.ErrUnauthorized)
	return false
}

func isDirectLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	peerIP := net.ParseIP(strings.Trim(host, "[]"))
	if peerIP == nil || !peerIP.IsLoopback() {
		return false
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			ip := net.ParseIP(strings.TrimSpace(part))
			if ip == nil || !ip.IsLoopback() {
				return false
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		ip := net.ParseIP(xri)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

type promSeriesKey struct {
	scope  string
	jobID  string
	unit   string
	source string
}

type promSeriesAcc struct {
	sum   float64
	count int
}

func sanitizePromName(raw string) string {
	var b strings.Builder
	for i, r := range raw {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':'
		isDigit := r >= '0' && r <= '9'
		if i == 0 && !isLetter {
			b.WriteByte('_')
		}
		if isLetter || isDigit {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "metric"
	}
	return b.String()
}

func escapePromLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	v = strings.ReplaceAll(v, "\r", `\r`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	var b strings.Builder
	for _, r := range v {
		if r < 0x20 && r != '\\' {
			fmt.Fprintf(&b, `\x%02x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func formatPrometheusText(samples []store.MetricSample) string {
	if len(samples) == 0 {
		return ""
	}
	grouped := make(map[string]map[promSeriesKey]*promSeriesAcc)
	units := make(map[string]string)
	for _, sample := range samples {
		name := sanitizePromName(sample.Name)
		if _, ok := grouped[name]; !ok {
			grouped[name] = make(map[promSeriesKey]*promSeriesAcc)
			units[name] = sample.Unit
		}
		key := promSeriesKey{
			scope:  sample.Scope,
			jobID:  sample.JobID,
			unit:   sample.Unit,
			source: sample.Source,
		}
		acc := grouped[name][key]
		if acc == nil {
			acc = &promSeriesAcc{}
			grouped[name][key] = acc
		}
		acc.sum += sample.Value
		acc.count++
	}

	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		seriesMap := grouped[name]
		if len(seriesMap) == 0 {
			continue
		}
		promType := "gauge"
		if units[name] == "count" {
			promType = "counter"
		}
		fmt.Fprintf(&b, "# HELP %s Measured pools-core %s metric (%s).\n", name, units[name], promType)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, promType)

		keys := make([]promSeriesKey, 0, len(seriesMap))
		for k := range seriesMap {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, c := keys[i], keys[j]
			if a.scope != c.scope {
				return a.scope < c.scope
			}
			if a.jobID != c.jobID {
				return a.jobID < c.jobID
			}
			if a.source != c.source {
				return a.source < c.source
			}
			return a.unit < c.unit
		})
		for _, k := range keys {
			acc := seriesMap[k]
			val := acc.sum
			if promType != "counter" {
				val = acc.sum / float64(acc.count)
			}
			labels := []string{
				fmt.Sprintf(`scope="%s"`, escapePromLabel(k.scope)),
				fmt.Sprintf(`source="%s"`, escapePromLabel(k.source)),
				fmt.Sprintf(`unit="%s"`, escapePromLabel(k.unit)),
			}
			if k.jobID != "" {
				labels = append(labels, fmt.Sprintf(`job_id="%s"`, escapePromLabel(k.jobID)))
			}
			fmt.Fprintf(&b, "%s{%s} %s\n", name, strings.Join(labels, ","), strconv.FormatFloat(val, 'g', -1, 64))
		}
	}
	return b.String()
}
