// Path: internal/pool/policy_F5.go | Codex | 2026-11-18
// T42 F5: resolve supported job controls under versioned admin limits.
package pool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"ccgsc/poolscore/internal/store"
)

// PolicyRule permits a developer override only when explicitly enabled. Numeric
// bounds and string enums apply to defaults and overrides alike; values are never clamped.
type PolicyRule struct {
	AllowOverride bool     `json:"allowOverride"`
	Min           *float64 `json:"min,omitempty"`
	Max           *float64 `json:"max,omitempty"`
	Allowed       []string `json:"allowed,omitempty"`
}
type PolicyConfig struct {
	AdminLimits map[string]PolicyRule      `json:"adminLimits"`
	PoolDefault map[string]json.RawMessage `json:"poolDefault"`
}
type PolicySelection struct {
	Values   map[string]json.RawMessage `json:"values"`
	ChosenBy map[string]string          `json:"chosenBy"`
}
type policyVersion struct {
	Page    string          `json:"page"`
	Version int             `json:"version"`
	Config  json.RawMessage `json:"config"`
}

var policyFields = []string{"policy", "needs", "priority", "maxAttempts", "batchSize", "threshold", "rerunPercent"}

func PolicyPage(p string) string {
	if p == Thread {
		return "thread-pool-admin"
	}
	if p == Process {
		return "process-pool-admin"
	}
	return ""
}

// DecodePolicyConfig reads only the F5 policy section of the existing page
// document. Other page controls retain their existing storage-only semantics.
func DecodePolicyConfig(page string, data json.RawMessage) (*PolicyConfig, error) {
	prefix := ""
	if page == PolicyPage(Thread) {
		prefix = "tp"
	}
	if page == PolicyPage(Process) {
		prefix = "pp"
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return nil, ErrInvalid
	}
	raw, ok := document[prefix+".policy"]
	if !ok {
		return nil, nil
	}
	if prefix == "" || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrInvalid
	}
	var c PolicyConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return nil, fmt.Errorf("%w: policy: %v", ErrInvalid, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalid
	}
	if len(c.AdminLimits) == 0 || len(c.AdminLimits) != len(c.PoolDefault) {
		return nil, ErrInvalid
	}
	for field, rule := range c.AdminLimits {
		if !slices.Contains(policyFields, field) {
			return nil, fmt.Errorf("%w: policy field %s", ErrUnsupported, field)
		}
		value, ok := c.PoolDefault[field]
		if !ok {
			return nil, ErrInvalid
		}
		if err := validatePolicyValue(field, value, rule); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// ValidateGPUAdminConfig validates the F5 page's GPU controls without making
// the older policy block mandatory.
func ValidateGPUAdminConfig(page string, data json.RawMessage) error {
	prefix := ""
	if page == PolicyPage(Thread) {
		prefix = "tp"
	} else if page == PolicyPage(Process) {
		prefix = "pp"
	} else {
		return nil
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return ErrInvalid
	}
	for key, raw := range document {
		if key != prefix+".gpu.enabled" && key != prefix+".gpu.max_slots_per_tab" && key != prefix+".gpu.on_battery_allowed" &&
			key != "gpuEnabled" && key != "maxGpuSlotsPerTab" && key != "gpuOnBatteryAllowed" {
			continue
		}
		switch key {
		case prefix + ".gpu.enabled", prefix + ".gpu.on_battery_allowed", "gpuEnabled", "gpuOnBatteryAllowed":
			var value bool
			if json.Unmarshal(raw, &value) != nil {
				return ErrInvalid
			}
		case prefix + ".gpu.max_slots_per_tab", "maxGpuSlotsPerTab":
			var value int
			if json.Unmarshal(raw, &value) != nil || value < 1 || value > 4 {
				return ErrInvalid
			}
		}
	}
	return nil
}

func DecodeGPUAdminConfig(page string, data json.RawMessage) (GPUConfig, bool, error) {
	cfg := DefaultGPUConfig()
	prefix := map[string]string{PolicyPage(Thread): "tp", PolicyPage(Process): "pp"}[page]
	if prefix == "" {
		return cfg, false, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return cfg, false, ErrInvalid
	}
	found := false
	for key, raw := range document {
		switch key {
		case prefix + ".gpu.enabled", "gpuEnabled":
			if err := json.Unmarshal(raw, &cfg.Enabled); err != nil {
				return cfg, false, ErrInvalid
			}
			found = true
		case prefix + ".gpu.max_slots_per_tab", "maxGpuSlotsPerTab":
			if err := json.Unmarshal(raw, &cfg.MaxSlotsPerTab); err != nil {
				return cfg, false, ErrInvalid
			}
			found = true
		case prefix + ".gpu.on_battery_allowed", "gpuOnBatteryAllowed":
			if err := json.Unmarshal(raw, &cfg.OnBatteryAllowed); err != nil {
				return cfg, false, ErrInvalid
			}
			found = true
		}
	}
	if found && (cfg.MaxSlotsPerTab < 1 || cfg.MaxSlotsPerTab > 4) {
		return cfg, false, ErrInvalid
	}
	return cfg, found, nil
}

func validatePolicyValue(field string, raw json.RawMessage, rule PolicyRule) error {
	bad := fmt.Errorf("%w: policy limit for %s", ErrInvalid, field)
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bad
	}
	var choices []string
	switch field {
	case "policy":
		choices = []string{"partial", "fail-fast", "threshold"}
	case "needs":
		choices = []string{"cpu", "gpu", "gpu-preferred"}
	case "priority":
		choices = []string{"low", "normal", "high"}
	}
	if choices != nil {
		var value string
		if json.Unmarshal(raw, &value) != nil || rule.Min != nil || rule.Max != nil || len(rule.Allowed) == 0 {
			return bad
		}
		for _, allowed := range rule.Allowed {
			if !slices.Contains(choices, allowed) {
				return bad
			}
		}
		if !slices.Contains(rule.Allowed, value) {
			return bad
		}
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || rule.Min == nil || rule.Max == nil || len(rule.Allowed) != 0 {
		return bad
	}
	lo, hi := 0.0, 100.0
	switch field {
	case "maxAttempts":
		lo, hi = 1, 10
	case "batchSize":
		lo, hi = 1, float64(MaxBatchSize)
	case "threshold":
		hi = 1
	}
	if math.IsNaN(*rule.Min) || math.IsNaN(*rule.Max) || *rule.Min < lo || *rule.Max > hi || *rule.Min > *rule.Max || value < *rule.Min || value > *rule.Max {
		return bad
	}
	if (field == "maxAttempts" || field == "batchSize") && (math.Trunc(value) != value || math.Trunc(*rule.Min) != *rule.Min || math.Trunc(*rule.Max) != *rule.Max) {
		return bad
	}
	return nil
}

// UnmarshalJSON preserves explicit zero for optional numeric policy overrides.
func (q *CreateRequest) UnmarshalJSON(data []byte) error {
	type wire CreateRequest
	var value wire
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*q = CreateRequest(value)
	q.policyPresent = make(map[string]bool)
	for _, field := range policyFields {
		if raw, ok := fields[field]; ok {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrInvalid
			}
			q.policyPresent[field] = true
		}
	}
	return nil
}

func (e *Engine) resolvePolicy(r store.Reader, p, owner string, q CreateRequest) (CreateRequest, string, *PolicySelection, error) {
	var v policyVersion
	err := r.Get(store.ConfigVersions, "active:"+PolicyPage(p), &v)
	if errors.Is(err, store.ErrNotFound) {
		return q, "builtin-v1", nil, nil
	}
	if err != nil {
		return q, "", nil, err
	}
	if v.Version < 1 || v.Page != PolicyPage(p) {
		return q, "", nil, ErrInvalid
	}
	config, err := DecodePolicyConfig(v.Page, v.Config)
	if err != nil {
		return q, "", nil, err
	}
	version := fmt.Sprintf("%s:%d", v.Page, v.Version)
	selected := &PolicySelection{Values: map[string]json.RawMessage{}, ChosenBy: map[string]string{}}
	if config == nil {
		return q, version, selected, nil
	}
	encoded, err := json.Marshal(q)
	if err != nil {
		return q, "", nil, err
	}
	var request map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &request); err != nil {
		return q, "", nil, err
	}
	for field, rule := range config.AdminLimits {
		value, explicit := request[field]
		if q.policyPresent[field] {
			explicit = true
			if len(value) == 0 {
				value = json.RawMessage("0")
			}
		}
		selected.ChosenBy[field] = "pool-default"
		if explicit {
			if !rule.AllowOverride {
				return q, "", nil, fmt.Errorf("%w: override disabled for %s", ErrForbidden, field)
			}
			selected.ChosenBy[field] = "developer:" + owner
		} else {
			value = config.PoolDefault[field]
		}
		if err = validatePolicyValue(field, value, rule); err != nil {
			return q, "", nil, err
		}
		request[field] = value
		selected.Values[field] = value
	}
	encoded, err = json.Marshal(request)
	if err != nil {
		return q, "", nil, err
	}
	if err = json.Unmarshal(encoded, &q); err != nil {
		return q, "", nil, err
	}
	if _, configured := config.AdminLimits["rerunPercent"]; configured && q.RerunPercent != nil && e.TrustConfig().EffectiveRerunPercent(*q.RerunPercent) != *q.RerunPercent {
		return q, "", nil, fmt.Errorf("%w: rerunPercent exceeds active verifier capability", ErrUnsupported)
	}
	return q, version, selected, nil
}

// Include explicitly supplied zero values in the idempotency hash; omitted and
// zero can choose different policy values even though the wire struct omits zero.
func policyRequestBytes(q CreateRequest) []byte {
	raw, _ := json.Marshal(q)
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	for field := range q.policyPresent {
		if _, ok := fields[field]; !ok {
			fields[field] = json.RawMessage("0")
		}
	}
	raw, _ = json.Marshal(fields)
	return raw
}

func (e *Engine) pinPolicy(tx store.Tx, j Job) error {
	if j.ChosenPolicy == nil {
		return nil
	}
	var version int
	if _, err := fmt.Sscanf(j.ConfigVersion, PolicyPage(j.PoolID)+":%d", &version); err != nil {
		return err
	}
	return tx.Put(store.ConfigVersions, "job:"+j.JobID, policyVersion{Page: PolicyPage(j.PoolID), Version: version})
}
