// Path: cmd/pools-browser-smoke/client.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the REST client of the harness. The developer key flow is the one
// of cmd/pools-e2e: the admin key is exchanged for a token, a developer key is
// created with it, and the developer key is exchanged for the token of every
// pool. The helpers of that flow are reused here, so the harness proves the
// same path an operator proves with the e2e binary.
//
// The client never prints a key. Every error carries a path and a status
// code, never key material.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// client is the REST client of one harness run.
type client struct {
	base string
	http *http.Client
	// stream is the client of the SSE stream: it has no overall deadline,
	// because the stream ends by the context and not by a timeout.
	stream *http.Client
}

// newClient answers a client for one base URL.
func newClient(base string) *client {
	return &client{
		base:   base,
		http:   &http.Client{Timeout: 2 * time.Minute},
		stream: &http.Client{},
	}
}

// createdKey is a key the harness created for itself.
type createdKey struct {
	raw   string
	keyID string
}

// do sends one request and answers the status, the body and the error. The
// body is kept on a non-2xx status, so the caller can report the RFC 9457
// problem detail of the server instead of a bare status code.
func (c *client) do(ctx context.Context, method, path, token string, body []byte, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: bad request: %w", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: request failed: %w", path, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, data, nil
}

// problemDetail answers the detail of an RFC 9457 problem body, so a failed
// request names the reason of the server and not only the status code.
func problemDetail(raw []byte) string {
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return strings.TrimSpace(string(raw))
	}
	switch {
	case p.Detail != "" && p.Title != "":
		return p.Title + ": " + p.Detail
	case p.Detail != "":
		return p.Detail
	case p.Title != "":
		return p.Title
	}
	return strings.TrimSpace(string(raw))
}

// exchange swaps an API key for an access token.
func (c *client) exchange(ctx context.Context, prefix, key string) (string, error) {
	status, body, err := c.do(ctx, http.MethodPost, prefix+"/auth/token", "", mustJSON(map[string]string{
		"apiKey": key,
	}), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s/auth/token: HTTP %d", prefix, status)
	}
	var tok struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("%s/auth/token: invalid token response", prefix)
	}
	return tok.AccessToken, nil
}

// createKey creates a key for the harness with the admin token. The scopes
// are the scopes of the developer key of cmd/pools-e2e.
func (c *client) createKey(ctx context.Context, prefix, adminToken, userID string, roles, scopes []string) (createdKey, error) {
	body := map[string]any{
		"user": map[string]any{
			"id":           userID,
			"accountStore": "B",
			"roles":        roles,
		},
		"scopes": scopes,
	}
	status, raw, err := c.do(ctx, http.MethodPost, prefix+"/auth/keys", adminToken, mustJSON(body), nil)
	if err != nil {
		return createdKey{}, err
	}
	if status != http.StatusCreated {
		return createdKey{}, fmt.Errorf("%s/auth/keys: HTTP %d", prefix, status)
	}
	var v struct {
		APIKey string `json:"apiKey"`
		KeyID  string `json:"keyId"`
	}
	if json.Unmarshal(raw, &v) != nil || v.APIKey == "" || v.KeyID == "" {
		return createdKey{}, fmt.Errorf("%s/auth/keys: invalid key response", prefix)
	}
	return createdKey{raw: v.APIKey, keyID: v.KeyID}, nil
}

// revokeKey revokes one key of the harness through the T99 route.
func (c *client) revokeKey(ctx context.Context, prefix, adminToken, keyID string) error {
	status, raw, err := c.do(ctx, http.MethodPost, prefix+"/auth/keys/"+keyID+":revoke", adminToken, nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s/auth/keys/%s:revoke: HTTP %d", prefix, keyID, status)
	}
	var v struct {
		KeyID   string `json:"keyId"`
		Revoked bool   `json:"revoked"`
	}
	if json.Unmarshal(raw, &v) != nil || !v.Revoked || v.KeyID != keyID {
		return fmt.Errorf("%s/auth/keys/%s:revoke: the response did not confirm the revocation", prefix, keyID)
	}
	return nil
}

// uploadArtifact uploads the module bytes of the harness. The bytes are
// unique per run, so the content-addressed blob of one run never collides
// with the blob of another run.
func (c *client) uploadArtifact(ctx context.Context, prefix, token string, data []byte, sum string) (int, error) {
	status, _, err := c.do(ctx, http.MethodPut, prefix+"/artifacts", token, data, map[string]string{
		"Content-Type":      "application/javascript",
		"X-Artifact-Sha256": sum,
		"Upload-Offset":     "0",
		"Upload-Length":     fmt.Sprint(len(data)),
	})
	return status, err
}

// jobResponse is the answer of a job creation.
type jobResponse struct {
	JobID string `json:"jobId"`
	State string `json:"state"`
}

// createJob submits one matmul job. The body is the one of cmd/pools-e2e:
// one module artifact, one task with the matmul input, batch size 1.
func (c *client) createJob(ctx context.Context, prefix, token, artifactID string, size int, taskID, idem string) (jobResponse, int, error) {
	body := map[string]any{
		"moduleArtifactIds": []string{artifactID},
		"tasks": []map[string]any{{
			"taskId": taskID,
			"input":  map[string]any{"json": map[string]any{"kind": "matmul-f32-gpu", "n": size}},
		}},
		"batchSize":    1,
		"needs":        "cpu",
		"rerunPercent": 0,
	}
	status, raw, err := c.do(ctx, http.MethodPost, prefix+"/jobs", token, mustJSON(body), map[string]string{
		"Idempotency-Key": idem,
	})
	if err != nil {
		return jobResponse{}, status, err
	}
	if status != http.StatusCreated {
		return jobResponse{}, status, fmt.Errorf("%s/jobs: HTTP %d: %s", prefix, status, problemDetail(raw))
	}
	var job jobResponse
	if json.Unmarshal(raw, &job) != nil || job.JobID == "" {
		return jobResponse{}, status, fmt.Errorf("%s/jobs: invalid job response", prefix)
	}
	return job, status, nil
}

// jobState reads the state of one job.
func (c *client) jobState(ctx context.Context, prefix, token, jobID string) (string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, prefix+"/jobs/"+jobID, token, nil, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s/jobs/%s: HTTP %d", prefix, jobID, status)
	}
	var job struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &job) != nil {
		return "", fmt.Errorf("%s/jobs/%s: invalid job response", prefix, jobID)
	}
	return job.State, nil
}

// awaitJob polls the job view until the job is terminal or the budget passes.
func (c *client) awaitJob(ctx context.Context, prefix, token, jobID string, budget time.Duration) (string, error) {
	deadline := time.Now().Add(budget)
	last := ""
	for {
		state, err := c.jobState(ctx, prefix, token, jobID)
		if err == nil {
			last = state
			switch state {
			case "completed", "failed", "partial", "cancelled":
				return state, nil
			}
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("job %s did not complete in time (last state %q)", jobID, last)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// jobResult is one settled result of a job.
type jobResult struct {
	TaskID string          `json:"taskId"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	SHA256 string          `json:"sha256"`
	HashOK bool            `json:"hashOk"`
}

// results reads the settled results of one job.
func (c *client) results(ctx context.Context, prefix, token, jobID string) ([]jobResult, error) {
	status, raw, err := c.do(ctx, http.MethodGet, prefix+"/jobs/"+jobID+"/results", token, nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s/jobs/%s/results: HTTP %d", prefix, jobID, status)
	}
	var page struct {
		Items []jobResult `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil, fmt.Errorf("%s/jobs/%s/results: invalid results response", prefix, jobID)
	}
	return page.Items, nil
}

// outputHash reads the hash the settled result carries. The REST result view
// wraps a plain JSON object output into the {"json":{...}} envelope of the
// TaskOutput contract, so both shapes are accepted.
func outputHash(result jobResult) (string, bool) {
	var envelope struct {
		JSON *struct {
			SHA256 string `json:"sha256"`
		} `json:"json"`
	}
	if json.Unmarshal(result.Output, &envelope) == nil && envelope.JSON != nil {
		return envelope.JSON.SHA256, true
	}
	var plain struct {
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(result.Output, &plain) == nil && plain.SHA256 != "" {
		return plain.SHA256, true
	}
	return "", false
}

// mustJSON renders a value as JSON.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// errNoContent marks the 204 answer of a lease pull.
var errNoContent = errors.New("no content")
