// Path: internal/grpcapi/convert.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the mapping between the proto messages and the engine types, so
// the gRPC surface reports exactly the same values as the REST surface of
// internal/api/api.go; WO T42 F10.
package grpcapi

import (
	"encoding/json"
	"strings"
	"time"

	"ccgsc/poolscore/internal/pool"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestamp converts a Go time into a proto timestamp. A zero time stays unset,
// which is how the REST surface reports an absent timestamp as null.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// timestampPtr converts an optional Go time into a proto timestamp.
func timestampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// structRaw converts a proto Struct back into the JSON bytes that the engine
// stores.
func structRaw(s *structpb.Struct) json.RawMessage {
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(s.AsMap())
	if err != nil {
		return nil
	}
	return raw
}

// groupByView converts the group-by placement of a job into its proto form.
func groupByView(raw json.RawMessage) *GroupBy {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var spec struct {
		Mode      string `json:"mode"`
		Key       string `json:"key"`
		MaxGroups int    `json:"maxGroups"`
	}
	if json.Unmarshal(raw, &spec) != nil {
		return nil
	}
	if spec.Mode == "" {
		spec.Mode = "none"
	}
	return &GroupBy{Mode: spec.Mode, Key: spec.Key, MaxGroups: int64(spec.MaxGroups)}
}

// pinView converts the pin placement of a job into its proto form.
func pinView(raw json.RawMessage) *Pin {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var spec struct {
		SessionID string   `json:"sessionId"`
		TabIDs    []string `json:"tabIds"`
		HostIDs   []string `json:"hostIds"`
	}
	if json.Unmarshal(raw, &spec) != nil {
		return nil
	}
	if spec.SessionID == "" && len(spec.TabIDs) == 0 && len(spec.HostIDs) == 0 {
		return nil
	}
	return &Pin{SessionId: spec.SessionID, TabIds: spec.TabIDs, HostIds: spec.HostIDs}
}

// policySelectionView converts the policy that the admin chose for a job into
// its proto form. The values are the raw JSON of every field, so the string
// form carries the exact bytes of the engine.
func policySelectionView(selection *pool.PolicySelection) *PolicySelection {
	if selection == nil {
		return nil
	}
	values := map[string]string{}
	for field, raw := range selection.Values {
		values[field] = string(raw)
	}
	chosen := map[string]string{}
	for field, layer := range selection.ChosenBy {
		chosen[field] = layer
	}
	return &PolicySelection{Values: values, ChosenBy: chosen}
}

// jobView converts a job of the engine into its proto form. The fields match
// the REST job view of internal/api/api.go.
func jobView(j pool.Job, rerunPercent float64) *Job {
	view := &Job{
		JobId:        j.JobID,
		ServerId:     j.ServerID,
		PoolId:       j.PoolID,
		State:        j.State,
		CreatedAt:    timestamp(j.CreatedAt),
		FinishedAt:   timestampPtr(j.FinishedAt),
		Needs:        j.Needs,
		Policy:       j.Policy,
		MaxAttempts:  int64(j.MaxAttempts),
		Priority:     j.Priority,
		SessionId:    j.SessionID,
		GroupBy:      groupByView(j.Request.GroupBy),
		Pin:          pinView(j.Request.Pin),
		RerunPercent: rerunPercent,
		Progress: &JobProgress{
			Total:     int64(j.Progress.Total),
			Completed: int64(j.Progress.Completed),
			Failed:    int64(j.Progress.Failed),
			Queued:    int64(j.Progress.Queued),
			Running:   int64(j.Progress.Running),
		},
		ConfigVersion: j.ConfigVersion,
		ChainMode:     string(j.ChainMode),
		ChainHead:     j.ChainHead,
		ChainEntries:  int64(j.ChainEntries),
		AuditGap:      j.AuditGap,
		ChosenPolicy:  policySelectionView(j.ChosenPolicy),
	}
	for _, w := range j.Warnings {
		view.Warnings = append(view.Warnings, &Warning{
			Code:     w.Code,
			Severity: w.Severity,
			Message:  w.Message,
		})
	}
	return view
}

// warningView converts the warnings of the engine into their proto form.
func warningView(warnings []pool.Warning) []*Warning {
	out := make([]*Warning, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, &Warning{Code: w.Code, Severity: w.Severity, Message: w.Message})
	}
	return out
}

// taskOutputView converts the output of a result into its proto form. The
// engine keeps the output as the raw JSON of the TaskOutput envelope, so the
// conversion reads the same envelope that the REST surface reads.
func taskOutputView(raw json.RawMessage) *TaskOutput {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Inline     *string         `json:"inline"`
		JSON       json.RawMessage `json:"json"`
		URL        *string         `json:"url"`
		ArtifactID *string         `json:"artifactId"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	switch {
	case envelope.Inline != nil:
		return &TaskOutput{Source: &TaskOutput_Inline{Inline: []byte(*envelope.Inline)}}
	case len(envelope.JSON) != 0:
		decoded := map[string]any{}
		if json.Unmarshal(envelope.JSON, &decoded) != nil {
			return nil
		}
		converted, err := structpb.NewStruct(decoded)
		if err != nil {
			return nil
		}
		return &TaskOutput{Source: &TaskOutput_Json{Json: converted}}
	case envelope.URL != nil:
		return &TaskOutput{Source: &TaskOutput_Url{Url: *envelope.URL}}
	case envelope.ArtifactID != nil:
		return &TaskOutput{Source: &TaskOutput_ArtifactId{ArtifactId: *envelope.ArtifactID}}
	}
	return nil
}

// taskResultView converts a result of the engine into its proto form. The
// shape matches the REST result view: the error is an ErrorDetail object and
// the completion time is the timestamp of the result.
func taskResultView(result pool.Result) *TaskResult {
	view := &TaskResult{
		JobId:       result.JobID,
		TaskId:      result.TaskID,
		Status:      result.Status,
		Attempts:    int64(result.Attempts),
		Output:      taskOutputView(result.Output),
		CompletedAt: timestamp(result.At),
		Late:        result.Late,
		Verification: &Verification{
			Rerun:         result.Rerun,
			HashMatch:     result.HashOK,
			CanaryRan:     result.CanaryRan,
			CanaryState:   result.CanaryState,
			CanaryOk:      result.CanaryOK,
			TrustComplete: result.TrustComplete,
			RerunOk:       result.RerunOK,
			Flagged:       result.Flagged,
			FlagReason:    result.FlagReason,
		},
	}
	if result.Error != "" {
		view.Error = &ErrorDetail{
			Code:      "task-failed",
			Type:      "about:blank",
			Title:     "Task failed",
			Status:    500,
			Message:   "Task failed",
			Detail:    result.Error,
			Retryable: false,
		}
	}
	return view
}

// artifactView converts an artifact of the engine into its proto form. The
// fields match the REST upload view, including the upload scan report and the
// admin review state.
func artifactView(a pool.Artifact) *Artifact {
	view := &Artifact{
		ArtifactId:    a.ArtifactID,
		Sha256:        a.SHA256,
		SizeBytes:     a.SizeBytes,
		ReceivedBytes: a.ReceivedBytes,
		NextOffset:    a.NextOffset,
		Complete:      a.Complete,
		ContentType:   a.ContentType,
		CreatedAt:     timestamp(a.CreatedAt),
		Owner:         a.Owner,
		ReviewState:   a.ReviewState,
		ReviewReason:  a.ReviewReason,
		ReviewQuorum:  int64(a.ReviewQuorum),
	}
	if a.Scan != nil {
		scan := &UploadScanReport{
			ArtifactId: a.Scan.ArtifactID,
			Kind:       a.Scan.Kind,
			SizeBytes:  a.Scan.SizeBytes,
			Sha256:     a.Scan.SHA256,
			Ok:         a.Scan.OK,
			Imports:    a.Scan.Imports,
			ScannedAt:  timestamp(a.Scan.ScannedAt),
		}
		for _, f := range a.Scan.Findings {
			scan.Findings = append(scan.Findings, &ScanFinding{
				Code:     f.Code,
				Severity: f.Severity,
				Message:  f.Message,
				Detail:   f.Detail,
			})
		}
		view.Scan = scan
	}
	return view
}

// sessionView converts a session of the engine into its proto form.
func sessionView(s pool.Session) *Session {
	view := &Session{
		SessionId:                 s.SessionID,
		State:                     s.State,
		TabCount:                  int64(s.TabCount),
		HostCount:                 int64(s.HostCount),
		Pin:                       pinView(s.Pin),
		WarmInitializerArtifactId: s.WarmInitializerArtifactID,
		WarmState:                 s.WarmState,
		CreatedAt:                 timestamp(s.CreatedAt),
		ExpiresAt:                 timestamp(s.ExpiresAt),
		Progress: &JobProgress{
			Total:     int64(s.Progress.Total),
			Completed: int64(s.Progress.Completed),
			Failed:    int64(s.Progress.Failed),
			Queued:    int64(s.Progress.Queued),
			Running:   int64(s.Progress.Running),
		},
		Warnings: warningView(s.Warnings),
	}
	if s.Pin != nil {
		view.Pinned = true
	}
	for _, tab := range s.Tabs {
		view.Tabs = append(view.Tabs, &SessionTab{
			TabId:    tab.TabID,
			HostId:   tab.HostID,
			WorkerId: tab.Owner,
			State:    tab.DeviceClass,
			Slots:    int64(tab.Slots),
		})
	}
	return view
}

// chainReportView converts the audit chain of a job into its proto form.
func chainReportView(report pool.ChainReport) *ChainReport {
	view := &ChainReport{
		JobId:    report.JobID,
		Mode:     string(report.Mode),
		Head:     report.Head,
		AuditGap: report.AuditGap,
		Verification: &ChainVerification{
			JobId:    report.Verification.JobID,
			Valid:    report.Verification.Valid,
			Mode:     string(report.Verification.Mode),
			Entries:  int64(report.Verification.Entries),
			Head:     report.Verification.Head,
			Reason:   report.Verification.Reason,
			BrokenAt: int64(report.Verification.BrokenAt),
		},
	}
	for _, entry := range report.Entries {
		view.Entries = append(view.Entries, &ChainEntry{
			Seq:          entry.Seq,
			JobId:        entry.JobID,
			TaskId:       entry.TaskID,
			ResultIndex:  int64(entry.ResultIndex),
			ResultSha256: entry.ResultSHA,
			WorkerId:     entry.WorkerID,
			At:           timestamp(entry.At),
			Prev:         entry.Prev,
			Hash:         entry.Hash,
		})
	}
	for _, entry := range report.RunEntries {
		view.RunEntries = append(view.RunEntries, &RunAuditEntry{
			Seq: entry.Seq, Kind: entry.Kind, JobId: entry.JobID, TaskId: entry.TaskID,
			RefIndex: int64(entry.RefIndex), Sha256: entry.Digest, WorkerId: entry.WorkerID,
			At: timestamp(entry.At), Prev: entry.Prev, Hash: entry.Hash,
		})
	}
	return view
}

// merkleReportView converts the global Merkle checkpoints into their proto
// form.
func merkleReportView(report pool.MerkleReport) *MerkleReport {
	view := &MerkleReport{LatestRoot: report.LatestRoot}
	for _, c := range report.Checkpoints {
		checkpoint := &MerkleCheckpoint{
			Seq:      c.Seq,
			At:       timestamp(c.At),
			Root:     c.Root,
			PrevRoot: c.PrevRoot,
			PrevHash: c.PrevHash,
			Hash:     c.Hash,
			JobCount: int64(c.JobCount),
		}
		for _, leaf := range c.Leaves {
			checkpoint.Leaves = append(checkpoint.Leaves, &MerkleLeaf{JobId: leaf.JobID, Tip: leaf.Tip})
		}
		view.Checkpoints = append(view.Checkpoints, checkpoint)
	}
	for _, v := range report.Verifications {
		view.Verifications = append(view.Verifications, &MerkleVerification{
			Seq:            v.Seq,
			Valid:          v.Valid,
			Root:           v.Root,
			RecomputedRoot: v.Recomputed,
			JobCount:       int64(v.JobCount),
			Reason:         v.Reason,
		})
	}
	return view
}
