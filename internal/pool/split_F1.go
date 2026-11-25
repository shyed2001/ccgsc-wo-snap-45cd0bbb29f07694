// T42 F1, 2026-11-18: bounded, deterministic artifact range/chunk expansion.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const MaxTasksPerJob = 10000
const MaxBatchSize = MaxTasksPerJob

// SplitInput describes a half-open slice. Bytes stay in the immutable artifact;
// range artifacts contain {"start":integer,"end":integer}, end exclusive.
type SplitInput struct {
	ArtifactID string      `json:"artifactId"`
	Format     string      `json:"format"`
	Offset     int64       `json:"offset,omitempty"`
	Length     int64       `json:"length,omitempty"`
	Range      *RangeInput `json:"json,omitempty"`
}

type RangeInput struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func validateSplitBatch(q CreateRequest) error {
	if q.BatchSize < 0 || q.BatchSize > MaxBatchSize {
		return ErrInvalid
	}
	for _, size := range q.Chunksize {
		if size < 1 {
			return ErrInvalid
		}
	}
	if q.Split == nil {
		if len(q.Chunksize) != 0 {
			return ErrUnsupported
		}
		return nil
	}
	s := q.Split
	id, err := hex.DecodeString(s.ArtifactID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != s.ArtifactID || s.Count < 1 || s.Count > MaxTasksPerJob || s.Chunksize < 0 {
		return ErrInvalid
	}
	if s.Format != "" && s.Format != "bytes" && s.Format != "range" {
		return ErrUnsupported
	}
	for id, size := range q.Chunksize {
		if id != s.ArtifactID {
			return ErrUnsupported
		}
		if s.Chunksize != 0 && size != s.Chunksize {
			return ErrInvalid
		}
	}
	return nil
}

func (e *Engine) expandTasks(r store.Reader, q CreateRequest) ([]TaskSpec, error) {
	if q.Split == nil {
		return q.Tasks, nil
	}
	s := *q.Split
	if s.Format == "" {
		s.Format = "bytes"
	}
	if s.Chunksize == 0 {
		s.Chunksize = q.Chunksize[s.ArtifactID]
	}
	var a Artifact
	if err := r.Get(store.Artifacts, s.ArtifactID, &a); err != nil {
		return nil, err
	}
	if !a.Complete {
		return nil, ErrConflict
	}
	start, length := int64(0), int64(len(a.Data))
	if s.Format == "range" {
		var bounds struct {
			Start *int64 `json:"start"`
			End   *int64 `json:"end"`
		}
		if err := json.Unmarshal(a.Data, &bounds); err != nil || bounds.Start == nil || bounds.End == nil {
			return nil, ErrInvalid
		}
		start, length = *bounds.Start, *bounds.End-*bounds.Start
		if *bounds.End <= start || length <= 0 { // also rejects signed subtraction overflow
			return nil, ErrInvalid
		}
	}
	if length < int64(s.Count) {
		return nil, ErrInvalid // never emit empty tasks
	}
	chunk := int64(s.Chunksize)
	if chunk > 0 && 1+(length-1)/chunk != int64(s.Count) {
		return nil, ErrInvalid // count and chunksize must describe the same partition
	}
	tasks := make([]TaskSpec, 0, s.Count)
	offset := int64(0)
	for i := 0; i < s.Count; i++ {
		size := chunk
		if size == 0 {
			size = length / int64(s.Count)
			if int64(i) < length%int64(s.Count) {
				size++
			}
		}
		if size > length-offset {
			size = length - offset
		}
		input := SplitInput{ArtifactID: s.ArtifactID, Format: s.Format}
		if s.Format == "range" {
			input.Range = &RangeInput{Start: start + offset, End: start + offset + size}
		} else {
			input.Offset, input.Length = offset, size
		}
		b, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, TaskSpec{TaskID: fmt.Sprintf("split-%05d", i+1), Input: b})
		offset += size
	}
	return tasks, nil
}
