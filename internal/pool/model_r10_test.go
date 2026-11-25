// Path: internal/pool/model_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MINOR row against Job.MarshalStoredJSON. The durable form
// drops the top-level results array from the plain marshal and re-appends the raw
// bytes, because byte-sensitive output must survive the store. The placeholder was
// removed with bytes.Replace over the WHOLE document, so a developer-supplied
// request metadata field that itself contains ,"results":null was rewritten
// first: the metadata lost that key, the top-level results stayed null, and the
// raw array was appended as a second, duplicate "results" member.
package pool

import (
	"encoding/json"
	"testing"
)

// TestR10StoredJSONKeepsUserMetadataWithResultsLiteral is the glm MINOR row.
func TestR10StoredJSONKeepsUserMetadataWithResultsLiteral(t *testing.T) {
	// The placeholder literal is reachable in metadata whenever one key precedes
	// a "results" key whose value is null.
	metadata := json.RawMessage(`{"note":"x","results":null,"keep":1}`)
	j := Job{
		JobID:   "job",
		Request: CreateRequest{Metadata: metadata},
		Results: []Result{{
			JobID: "job", TaskID: "task", Status: "done",
			Output: json.RawMessage(`{"answer":42}`),
		}},
	}
	raw, err := j.MarshalStoredJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Job
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("stored job is not valid JSON: %v: %s", err, raw)
	}
	if got := string(back.Request.Metadata); got != string(metadata) {
		t.Fatalf("metadata = %s, want %s (stored: %s)", got, metadata, raw)
	}
	if len(back.Results) != 1 {
		t.Fatalf("results = %d, want the one raw result (stored: %s)", len(back.Results), raw)
	}
	if got := string(back.Results[0].Output); got != `{"answer":42}` {
		t.Fatalf("result output = %s, want the exact bytes", got)
	}
}

// TestR10StoredJSONHasExactlyOneResultsMember pins the shape the replacement is
// meant to produce: one top-level "results" member, never a duplicate.
func TestR10StoredJSONHasExactlyOneResultsMember(t *testing.T) {
	j := Job{
		JobID:   "job",
		Request: CreateRequest{Metadata: json.RawMessage(`{"note":"x","results":null,"keep":1}`)},
		Results: []Result{{JobID: "job", TaskID: "task", Status: "done", Output: json.RawMessage(`{"answer":42}`)}},
	}
	raw, err := j.MarshalStoredJSON()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("stored job is not valid JSON: %v: %s", err, raw)
	}
	want := `[{"jobId":"job","taskId":"task","status":"done","attempts":0,"at":"0001-01-01T00:00:00Z","output":{"answer":42}}]`
	if got := string(top["results"]); got != want {
		t.Fatalf("top-level results = %s, want %s (stored: %s)", got, want, raw)
	}
}

// TestR10StoredJSONRoundTripsMetadataWithoutResults pins the ordinary path: no
// results means no placeholder rewrite at all.
func TestR10StoredJSONRoundTripsMetadataWithoutResults(t *testing.T) {
	j := Job{JobID: "job", Request: CreateRequest{Metadata: json.RawMessage(`{"k":"v"}`)}}
	raw, err := j.MarshalStoredJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Job
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("stored job is not valid JSON: %v: %s", err, raw)
	}
	if got := string(back.Request.Metadata); got != `{"k":"v"}` {
		t.Fatalf("metadata = %s, want the input", got)
	}
}
