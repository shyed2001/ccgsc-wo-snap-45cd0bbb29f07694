// Path: internal/store/metrics_MVPW.go
// What/why: Slice B metric sample model, filter, and store transaction helpers (WWPP-R37.2, R40.1).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// MetricSampleKeyPrefix identifies pool_metrics sample records routed through store.Metrics.
const MetricSampleKeyPrefix = "sample:"

// MetricTimeLayout uses fixed-width UTC nanoseconds so SQLite text ordering matches chronology.
const MetricTimeLayout = "2006-01-02T15:04:05.000000000Z"

var metricSampleSeq atomic.Uint64

// MetricSample is one persisted measurement sample matching pool_metrics.
type MetricSample struct {
	TS     time.Time `json:"ts"`
	Scope  string    `json:"scope"`
	JobID  string    `json:"jobId,omitempty"`
	Name   string    `json:"name"`
	Value  float64   `json:"value"`
	Unit   string    `json:"unit"`
	Source string    `json:"source"`
}

// MetricFilter narrows metric sample queries by scope, job id, and earliest timestamp.
type MetricFilter struct {
	Scope string
	JobID string
	Since time.Time
}

// MetricQueryReader is optionally implemented by transactional readers backed by pool_metrics.
type MetricQueryReader interface {
	QueryMetricSamples(filter MetricFilter) ([]MetricSample, error)
}

// NormalizeMetricScope folds common pool aliases while keeping custom scopes exact.
func NormalizeMetricScope(scope string) string {
	s := strings.ToLower(strings.TrimSpace(scope))
	switch s {
	case "tp", "thread", "thread-pool", "threadpool":
		return "tp"
	case "pp", "process", "process-pool", "processpool":
		return "pp"
	default:
		return s
	}
}

// MatchesMetricScope compares two scope labels with pool alias folding.
func MatchesMetricScope(sampleScope, filterScope string) bool {
	if strings.TrimSpace(filterScope) == "" {
		return true
	}
	return NormalizeMetricScope(sampleScope) == NormalizeMetricScope(filterScope)
}

// ScopeCandidates expands a scope filter to equivalent stored aliases for SQL filtering.
func ScopeCandidates(filterScope string) []string {
	norm := NormalizeMetricScope(filterScope)
	switch norm {
	case "":
		return nil
	case "tp":
		return []string{"tp", "thread", "thread-pool", "threadpool"}
	case "pp":
		return []string{"pp", "process", "process-pool", "processpool"}
	default:
		trimmed := strings.TrimSpace(filterScope)
		if trimmed == norm {
			return []string{norm}
		}
		return []string{trimmed, norm}
	}
}

// NormalizeMetricSample returns a sanitized copy ready for insertion.
func NormalizeMetricSample(s MetricSample) (MetricSample, error) {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return MetricSample{}, errors.New("metric name is required")
	}
	if s.TS.IsZero() {
		s.TS = time.Now().UTC()
	} else {
		s.TS = s.TS.UTC()
	}
	s.Scope = strings.TrimSpace(s.Scope)
	if s.Scope == "" {
		s.Scope = "global"
	}
	s.JobID = strings.TrimSpace(s.JobID)
	s.Unit = strings.TrimSpace(s.Unit)
	if s.Unit == "" {
		s.Unit = "count"
	}
	s.Source = strings.TrimSpace(s.Source)
	if s.Source == "" {
		s.Source = "pool"
	}
	return s, nil
}

// DecodeMetricSample decodes either a value/pointer MetricSample or marshaled JSON bytes.
func DecodeMetricSample(v any) (MetricSample, error) {
	switch x := v.(type) {
	case MetricSample:
		return NormalizeMetricSample(x)
	case *MetricSample:
		if x == nil {
			return MetricSample{}, errors.New("nil metric sample")
		}
		return NormalizeMetricSample(*x)
	case []byte:
		var s MetricSample
		if err := json.Unmarshal(x, &s); err != nil {
			return MetricSample{}, err
		}
		return NormalizeMetricSample(s)
	case json.RawMessage:
		var s MetricSample
		if err := json.Unmarshal(x, &s); err != nil {
			return MetricSample{}, err
		}
		return NormalizeMetricSample(s)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return MetricSample{}, err
		}
		var s MetricSample
		if err := json.Unmarshal(b, &s); err != nil {
			return MetricSample{}, err
		}
		return NormalizeMetricSample(s)
	}
}

// RecordMetricSample writes one sample into pool_metrics inside an existing store transaction.
func RecordMetricSample(tx Tx, s MetricSample) error {
	if tx == nil {
		return errors.New("store transaction is nil")
	}
	norm, err := NormalizeMetricSample(s)
	if err != nil {
		return err
	}
	seq := metricSampleSeq.Add(1)
	key := fmt.Sprintf("%s%020d:%012d", MetricSampleKeyPrefix, norm.TS.UnixNano(), seq)
	return tx.Put(Metrics, key, norm)
}

// QueryMetricSamples loads samples matching filter ordered chronologically.
func QueryMetricSamples(ctx context.Context, s Store, filter MetricFilter) ([]MetricSample, error) {
	if s == nil {
		return nil, nil
	}
	var out []MetricSample
	err := s.View(ctx, func(rd Reader) error {
		if q, ok := rd.(MetricQueryReader); ok {
			var err error
			out, err = q.QueryMetricSamples(filter)
			return err
		}
		records, err := rd.List(Metrics)
		if err != nil {
			return err
		}
		out = make([]MetricSample, 0)
		for _, rec := range records {
			if !strings.HasPrefix(rec.Key, MetricSampleKeyPrefix) {
				continue
			}
			var sample MetricSample
			if err := json.Unmarshal(rec.Value, &sample); err != nil {
				continue
			}
			if !MatchesMetricScope(sample.Scope, filter.Scope) {
				continue
			}
			if filter.JobID != "" && sample.JobID != filter.JobID {
				continue
			}
			if !filter.Since.IsZero() && sample.TS.Before(filter.Since.UTC()) {
				continue
			}
			out = append(out, sample)
		}
		sort.Slice(out, func(i, j int) bool {
			if !out[i].TS.Equal(out[j].TS) {
				return out[i].TS.Before(out[j].TS)
			}
			return out[i].Name < out[j].Name
		})
		return nil
	})
	return out, err
}
