package pool

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
)

type groupBySpec struct {
	Mode      string `json:"mode"`
	Key       string `json:"key,omitempty"`
	MaxGroups int    `json:"maxGroups,omitempty"`
}

type pinSpec struct {
	SessionID string   `json:"sessionId,omitempty"`
	TabIDs    []string `json:"tabIds,omitempty"`
	HostIDs   []string `json:"hostIds,omitempty"`
}

func decodeGroupBy(raw json.RawMessage) (groupBySpec, error) {
	var spec groupBySpec
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return spec, nil
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, ErrInvalid
	}
	if spec.Mode == "" {
		spec.Mode = "none"
	}
	if spec.Mode != "none" && spec.Mode != "tab" && spec.Mode != "host" {
		return spec, ErrInvalid
	}
	if spec.Mode != "none" && spec.Key == "" {
		return spec, ErrInvalid
	}
	if spec.MaxGroups < 0 {
		return spec, ErrInvalid
	}
	return spec, nil
}

func decodePin(raw json.RawMessage) (pinSpec, error) {
	var spec pinSpec
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return spec, nil
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, ErrInvalid
	}
	if spec.SessionID == "" && len(spec.TabIDs) == 0 && len(spec.HostIDs) == 0 {
		return spec, ErrInvalid
	}
	if spec.SessionID != "" && (len(spec.TabIDs) > 0 || len(spec.HostIDs) > 0) {
		return spec, ErrInvalid
	}
	return spec, nil
}

func (e *Engine) validatePlacement(q CreateRequest) error {
	group, err := decodeGroupBy(q.GroupBy)
	if err != nil {
		return err
	}
	pin, err := decodePin(q.Pin)
	if err != nil {
		return err
	}
	if pin.SessionID != "" && group.Mode == "host" {
		return ErrInvalid
	}

	if group.MaxGroups > 0 && group.Mode == "none" {
		return ErrInvalid
	}
	return nil
}

func isArtifactID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (e *Engine) pinAllows(pinRaw json.RawMessage, w Worker) bool {
	pin, err := decodePin(pinRaw)
	if err != nil {
		return false
	}
	if pin.SessionID != "" && w.SessionID != pin.SessionID {
		return false
	}
	if len(pin.TabIDs) > 0 && !contains(pin.TabIDs, w.TabID) {
		return false
	}
	if len(pin.HostIDs) > 0 && !contains(pin.HostIDs, w.HostID) {
		return false
	}
	return true
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func groupValue(spec groupBySpec, task TaskSpec) (string, bool) {
	if spec.Mode == "none" {
		return "", true
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(task.Input, &value); err != nil {
		return "", false
	}
	raw, ok := value[spec.Key]
	if !ok {
		return "", false
	}
	return string(raw), true
}

func (e *Engine) placementAllows(j *Job, t *Task, w Worker, selected string) (string, bool) {
	if len(j.Request.GroupBy) == 0 && len(j.Request.Pin) == 0 {
		return selected, true
	}
	if !e.pinAllows(j.Request.Pin, w) {
		return selected, false
	}
	group, err := decodeGroupBy(j.Request.GroupBy)
	if err != nil {
		return selected, false
	}
	value, ok := groupValue(group, t.TaskSpec)
	if !ok {
		return selected, false
	}
	if selected != "" && value != selected {
		return selected, false
	}
	return value, true
}
