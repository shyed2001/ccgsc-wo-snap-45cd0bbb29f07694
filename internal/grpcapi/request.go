// Path: internal/grpcapi/request.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the mapping from the proto request messages to the engine request
// types, with the presence of the policy fields preserved, so the admin policy
// resolves exactly as it does on the REST surface; WO T42 F10.
package grpcapi

import (
	"encoding/json"

	"ccgsc/poolscore/internal/pool"
)

// createRequest converts a CreateJobRequest into the engine request. The seven
// policy fields keep their presence, because the engine tells "omitted" from
// "explicit zero" apart while it resolves the admin policy.
func createRequest(req *CreateJobRequest) (pool.CreateRequest, error) {
	out := pool.CreateRequest{
		ModuleArtifactIDs: req.GetModuleArtifactIds(),
		ModuleSHA256:      req.GetModuleSha256(),
		BatchSize:         int(req.GetBatchSize()),
		Needs:             req.GetNeeds(),
		Policy:            req.GetPolicy(),
		Threshold:         req.GetThreshold(),
		MaxAttempts:       int(req.GetMaxAttempts()),
		Priority:          req.GetPriority(),
		Metadata:          structRaw(req.GetMetadata()),
	}
	if req.RerunPercent != nil {
		value := req.GetRerunPercent()
		out.RerunPercent = &value
	}
	if req.GetSplit() != nil {
		s := req.GetSplit()
		out.Split = &pool.SplitSpec{
			ArtifactID: s.GetArtifactId(),
			Count:      int(s.GetCount()),
			Format:     s.GetFormat(),
			Chunksize:  int(s.GetChunksize()),
		}
	}
	for _, t := range req.GetTasks() {
		spec := pool.TaskSpec{TaskID: t.GetTaskId(), TimeoutMS: int(t.GetTimeoutMs())}
		input, err := taskInput(t.GetInput())
		if err != nil {
			return pool.CreateRequest{}, err
		}
		spec.Input = input
		out.Tasks = append(out.Tasks, spec)
	}
	if len(req.GetChunksize()) > 0 {
		out.Chunksize = map[string]int{}
		for id, size := range req.GetChunksize() {
			out.Chunksize[id] = int(size)
		}
	}
	if g := req.GetGroupBy(); g != nil {
		raw, err := json.Marshal(map[string]any{
			"mode":      g.GetMode(),
			"key":       g.GetKey(),
			"maxGroups": g.GetMaxGroups(),
		})
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.GroupBy = raw
	}
	if p := req.GetPin(); p != nil {
		raw, err := json.Marshal(map[string]any{
			"sessionId": p.GetSessionId(),
			"tabIds":    p.GetTabIds(),
			"hostIds":   p.GetHostIds(),
		})
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.Pin = raw
	}
	if o := req.GetOptions(); o != nil {
		raw, err := json.Marshal(optionsWire(o))
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.Options = raw
	}
	if w := req.GetWebhook(); w != nil {
		out.Webhook = &pool.WebhookConfig{URL: w.GetUrl(), Secret: w.GetSecret()}
	}
	return out, nil
}

// optionsWire renders the developer options into the JSON shape that the
// engine parses. The option fields that the engine does not support yet keep
// their value, so the engine refuses them with ErrUnsupported instead of the
// gRPC surface dropping them silently.
func optionsWire(o *JobOptions) map[string]any {
	out := map[string]any{}
	if o.GetTaskTimeoutMs() != 0 {
		out["taskTimeoutMs"] = o.GetTaskTimeoutMs()
	}
	if o.GetMaxTaskBytes() != 0 {
		out["maxTaskBytes"] = o.GetMaxTaskBytes()
	}
	if len(o.GetTags()) > 0 {
		out["tags"] = o.GetTags()
	}
	if o.GetToleranceCompare() {
		out["toleranceCompare"] = true
	}
	if o.GetComparatorArtifactId() != "" {
		out["comparatorArtifactId"] = o.GetComparatorArtifactId()
	}
	if t := o.GetTamperEvidence(); t != nil {
		out["tamperEvidence"] = map[string]any{
			"mode":        t.GetMode(),
			"chainPerJob": t.GetChainPerJob(),
		}
	}
	if u := o.GetUploadChecks(); u != nil {
		out["uploadChecks"] = map[string]any{
			"mode":          u.GetMode(),
			"requireReview": u.GetRequireReview(),
		}
	}
	if p := o.GetP2P(); p != nil {
		out["p2p"] = map[string]any{
			"enabled":         p.GetEnabled(),
			"peers":           p.GetPeers(),
			"shareMode":       p.GetShareMode(),
			"resultsToServer": p.GetResultsToServer(),
		}
	}
	if d := o.GetDew(); d != nil {
		out["dew"] = map[string]any{
			"enabled":              d.GetEnabled(),
			"receiptWindowSeconds": d.GetReceiptWindowSeconds(),
		}
	}
	return out
}

// taskInput converts the input of a task into the raw JSON that the engine
// stores. The envelope is the same one that the REST surface accepts: inline,
// json or artifactId.
func taskInput(input *TaskInput) (json.RawMessage, error) {
	if input == nil {
		return nil, nil
	}
	envelope := map[string]any{}
	switch source := input.GetSource().(type) {
	case *TaskInput_Inline:
		envelope["inline"] = string(source.Inline)
	case *TaskInput_Json:
		envelope["json"] = source.Json.AsMap()
	case *TaskInput_ArtifactId:
		envelope["artifactId"] = source.ArtifactId
	default:
		return nil, nil
	}
	return json.Marshal(envelope)
}

// sessionRequest converts an OpenSessionRequest into the engine request.
func sessionRequest(req *OpenSessionRequest) (pool.SessionRequest, error) {
	out := pool.SessionRequest{
		TabCount:                  int(req.GetTabCount()),
		HostCount:                 int(req.GetHostCount()),
		TTLSeconds:                int(req.GetTtlSeconds()),
		Needs:                     req.GetNeeds(),
		ModuleArtifactIDs:         req.GetModuleArtifactIds(),
		WarmInitializerArtifactID: req.GetWarmInitializerArtifactId(),
		Metadata:                  structRaw(req.GetMetadata()),
	}
	if p := req.GetPin(); p != nil {
		raw, err := json.Marshal(map[string]any{
			"sessionId": p.GetSessionId(),
			"tabIds":    p.GetTabIds(),
			"hostIds":   p.GetHostIds(),
		})
		if err != nil {
			return pool.SessionRequest{}, err
		}
		out.Pin = raw
	}
	if o := req.GetOptions(); o != nil {
		raw, err := json.Marshal(map[string]any{
			"dew":          dewWire(o.GetDew()),
			"p2p":          p2pWire(o.GetP2P()),
			"uploadChecks": uploadChecksWire(o.GetUploadChecks()),
		})
		if err != nil {
			return pool.SessionRequest{}, err
		}
		out.Options = raw
	}
	return out, nil
}

// dewWire renders the dew option into the JSON shape of the engine.
func dewWire(d *DewOptions) map[string]any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"enabled":              d.GetEnabled(),
		"receiptWindowSeconds": d.GetReceiptWindowSeconds(),
	}
}

// p2pWire renders the peer-to-peer option into the JSON shape of the engine.
func p2pWire(p *P2POptions) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"enabled":         p.GetEnabled(),
		"peers":           p.GetPeers(),
		"shareMode":       p.GetShareMode(),
		"resultsToServer": p.GetResultsToServer(),
	}
}

// uploadChecksWire renders the upload check option into the JSON shape of the
// engine.
func uploadChecksWire(u *UploadCheckOption) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"mode":          u.GetMode(),
		"requireReview": u.GetRequireReview(),
	}
}

// sessionTaskRequest converts a StreamSessionTasksRequest into the engine job
// request that the session batch uses.
func sessionTaskRequest(req *StreamSessionTasksRequest) (pool.CreateRequest, error) {
	out := pool.CreateRequest{
		BatchSize:   int(req.GetBatchSize()),
		Needs:       req.GetNeeds(),
		Policy:      req.GetPolicy(),
		Threshold:   req.GetThreshold(),
		MaxAttempts: int(req.GetMaxAttempts()),
		Priority:    req.GetPriority(),
		Metadata:    structRaw(req.GetMetadata()),
	}
	if req.GetRerunPercent() != 0 {
		value := req.GetRerunPercent()
		out.RerunPercent = &value
	}
	for _, t := range req.GetTasks() {
		spec := pool.TaskSpec{TaskID: t.GetTaskId(), TimeoutMS: int(t.GetTimeoutMs())}
		input, err := taskInput(t.GetInput())
		if err != nil {
			return pool.CreateRequest{}, err
		}
		spec.Input = input
		out.Tasks = append(out.Tasks, spec)
	}
	if len(req.GetChunksize()) > 0 {
		out.Chunksize = map[string]int{}
		for id, size := range req.GetChunksize() {
			out.Chunksize[id] = int(size)
		}
	}
	if g := req.GetGroupBy(); g != nil {
		raw, err := json.Marshal(map[string]any{
			"mode":      g.GetMode(),
			"key":       g.GetKey(),
			"maxGroups": g.GetMaxGroups(),
		})
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.GroupBy = raw
	}
	if p := req.GetPin(); p != nil {
		raw, err := json.Marshal(map[string]any{
			"sessionId": p.GetSessionId(),
			"tabIds":    p.GetTabIds(),
			"hostIds":   p.GetHostIds(),
		})
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.Pin = raw
	}
	if o := req.GetOptions(); o != nil {
		raw, err := json.Marshal(map[string]any{
			"taskTimeoutMs":        o.GetTaskTimeoutMs(),
			"maxTaskBytes":         o.GetMaxTaskBytes(),
			"tags":                 o.GetTags(),
			"toleranceCompare":     o.GetToleranceCompare(),
			"comparatorArtifactId": o.GetComparatorArtifactId(),
			"tamperEvidence":       tamperWire(o.GetTamperEvidence()),
			"dew":                  dewWire(o.GetDew()),
			"p2p":                  p2pWire(o.GetP2P()),
		})
		if err != nil {
			return pool.CreateRequest{}, err
		}
		out.Options = raw
	}
	return out, nil
}

// tamperWire renders the tamper evidence option into the JSON shape of the
// engine.
func tamperWire(t *TamperEvidenceOption) map[string]any {
	if t == nil {
		return nil
	}
	return map[string]any{
		"mode":        t.GetMode(),
		"chainPerJob": t.GetChainPerJob(),
	}
}
