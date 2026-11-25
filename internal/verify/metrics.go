// Path: internal/verify/metrics.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: light metrics for every verification check (duration ns + outcome counters);
// WWPP-R37.2 (built-in light metrics always on), R40.1 (Trust counters on the monitor page).
package verify

import (
	"sync"
	"time"
)

// Metric operation names used by the light metrics registry (WWPP-R37.2).
const (
	OpHash       = "hash"
	OpEnvelope   = "envelope"
	OpRerun      = "rerun"
	OpCanary     = "canary"
	OpReputation = "reputation"
)

// OpStats holds the counters and timings of one verification operation.
type OpStats struct {
	Checks  uint64 `json:"checks"`  // total observations
	Pass    uint64 `json:"pass"`    // successful observations
	Fail    uint64 `json:"fail"`    // failed observations
	TotalNS uint64 `json:"totalNs"` // summed duration in nanoseconds
	LastNS  int64  `json:"lastNs"`  // duration of the most recent observation
	MaxNS   int64  `json:"maxNs"`   // slowest observation so far
	MinNS   int64  `json:"minNs"`   // fastest observation so far
	Zeros   uint64 `json:"zeros"`   // observations the clock could not resolve (0 ns)
}

// AvgNS returns the mean observation duration in nanoseconds (0 when there is no sample).
func (o OpStats) AvgNS() float64 {
	if o.Checks == 0 {
		return 0
	}
	return float64(o.TotalNS) / float64(o.Checks)
}

// StatsSnapshot is an immutable copy of a metric registry.
type StatsSnapshot struct {
	Ops         map[string]OpStats `json:"ops"`
	TotalChecks uint64             `json:"totalChecks"`
	TotalPass   uint64             `json:"totalPass"`
	TotalFail   uint64             `json:"totalFail"`
	TotalNS     uint64             `json:"totalNs"`
}

// Metrics is a concurrency-safe registry of verification counters and durations.
//
// The clock is injectable so the measurements are deterministic in tests and so a host
// with a coarse system clock can be told apart from a genuinely fast check: an
// observation whose duration resolves to 0 ns is counted in OpStats.Zeros.
type Metrics struct {
	mu    sync.Mutex
	ops   map[string]*OpStats
	clock func() time.Time
}

// NewMetrics returns an empty registry using the wall clock.
func NewMetrics() *Metrics {
	return &Metrics{ops: make(map[string]*OpStats)}
}

// SetClock replaces the clock used for the measurements (tests, benchmarks).
func (m *Metrics) SetClock(f func() time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f == nil {
		f = time.Now
	}
	m.clock = f
}

// Now returns the current time of the metrics clock.
func (m *Metrics) Now() time.Time {
	if m == nil {
		return defaultMetrics.Now()
	}
	m.mu.Lock()
	clock := m.clock
	m.mu.Unlock()
	if clock == nil {
		return time.Now()
	}
	return clock()
}

// Start marks the beginning of a measured operation.
func (m *Metrics) Start() time.Time { return m.Now() }

// Record adds one observation: the operation name, its duration and the outcome.
func (m *Metrics) Record(op string, d time.Duration, ok bool) {
	if m == nil || op == "" {
		return
	}
	ns := d.Nanoseconds()
	if ns < 0 {
		ns = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.ops[op]
	if o == nil {
		o = &OpStats{}
		m.ops[op] = o
	}
	o.Checks++
	if ok {
		o.Pass++
	} else {
		o.Fail++
	}
	o.TotalNS += uint64(ns)
	o.LastNS = ns
	if ns > o.MaxNS {
		o.MaxNS = ns
	}
	if o.Checks == 1 || ns < o.MinNS {
		o.MinNS = ns
	}
	if ns == 0 {
		o.Zeros++
	}
}

// RecordSince adds one observation measured from start.
func (m *Metrics) RecordSince(op string, start time.Time, ok bool) {
	m.Record(op, m.Now().Sub(start), ok)
}

// Snapshot copies the registry contents.
func (m *Metrics) Snapshot() StatsSnapshot {
	if m == nil {
		return StatsSnapshot{Ops: map[string]OpStats{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := StatsSnapshot{Ops: make(map[string]OpStats, len(m.ops))}
	for k, v := range m.ops {
		out.Ops[k] = *v
		out.TotalChecks += v.Checks
		out.TotalPass += v.Pass
		out.TotalFail += v.Fail
		out.TotalNS += v.TotalNS
	}
	return out
}

// Reset clears every counter.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = make(map[string]*OpStats)
}

// defaultMetrics is the process-wide registry used by the package level checks.
var defaultMetrics = NewMetrics()

// DefaultMetrics returns the process-wide registry (the one the package level checks write to).
func DefaultMetrics() *Metrics { return defaultMetrics }

// Stats returns a snapshot of the process-wide registry (WWPP-R37.2 light metrics).
func Stats() StatsSnapshot { return defaultMetrics.Snapshot() }

// ResetStats clears the process-wide registry (test helper).
func ResetStats() { defaultMetrics.Reset() }

// startOn returns the start timestamp of a measured operation on m, or on the
// process-wide registry when m is nil.
func startOn(m *Metrics) time.Time {
	if m == nil {
		return defaultMetrics.Start()
	}
	return m.Start()
}

// recordOn records one observation on m, or on the process-wide registry when m is nil.
func recordOn(m *Metrics, op string, start time.Time, ok bool) {
	if m == nil {
		defaultMetrics.RecordSince(op, start, ok)
		return
	}
	m.RecordSince(op, start, ok)
}
