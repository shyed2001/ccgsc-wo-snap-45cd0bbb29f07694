// Path: tests/upload_scan_F8_test.go | Author: hermes (MiniMax-M3) for the owner | 2026-11-18
// What/why: T42 F8 end-to-end HTTP tests of the module upload scan and the admin
// review gate: the upload route reports the scan, the admin routes list the queue,
// record one decision and read or write the scan configuration.
package tests

import (
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"testing"
)

type f8ArtifactView struct {
	ArtifactID   string `json:"artifactId"`
	Complete     bool   `json:"complete"`
	ReviewState  string `json:"reviewState"`
	ReviewReason string `json:"reviewReason"`
	ReviewQuorum int    `json:"reviewQuorum"`
	Scan         *struct {
		OK       bool     `json:"ok"`
		Kind     string   `json:"kind"`
		Imports  []string `json:"imports"`
		Findings []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"findings"`
	} `json:"scan"`
}

func TestF8UploadScanReportsFindingsOverHTTP(t *testing.T) {
	f := setup(t)
	_, dev := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		// A plain data artifact passes the scan and reports its review state.
		data := []byte("plain data module")
		id := verify.SHA256(data)
		body := f.request(t, "PUT", base+"/artifacts", dev, data, 201, map[string]string{"X-Artifact-Sha256": id})
		view := decode[f8ArtifactView](t, body)
		if !view.Complete || view.Scan == nil || !view.Scan.OK {
			t.Fatalf("%s: scan result = %+v", base, view)
		}
		if view.ReviewState != pool.ReviewNotRequired {
			t.Fatalf("%s: review state = %q", base, view.ReviewState)
		}
		// The capabilities route advertises the scan truthfully.
		caps := decode[struct {
			UploadChecks struct {
				FormatSizeHash       bool `json:"formatSizeHash"`
				ImportPermissionScan bool `json:"importPermissionScan"`
				ReviewGate           bool `json:"reviewGate"`
				MalwareAbuseScan     bool `json:"malwareAbuseScan"`
			} `json:"uploadChecks"`
			ServerLimits struct {
				MaxArtifactBytes int64 `json:"maxArtifactBytes"`
			} `json:"serverLimits"`
		}](t, f.request(t, "GET", base+"/capabilities", dev, nil, 200, nil))
		if !caps.UploadChecks.FormatSizeHash || !caps.UploadChecks.ImportPermissionScan ||
			caps.UploadChecks.ReviewGate || caps.UploadChecks.MalwareAbuseScan ||
			caps.ServerLimits.MaxArtifactBytes != pool.MaxArtifactBytes {
			t.Fatalf("%s: capabilities = %+v limits=%+v", base, caps.UploadChecks, caps.ServerLimits)
		}
		// A developer may not read the admin review queue.
		f.request(t, "GET", base+"/admin/reviews", dev, nil, 403, nil)
		// The admin reads the queue.
		queue := decode[struct {
			Items []f8ArtifactView `json:"items"`
			Count int              `json:"count"`
		}](t, f.request(t, "GET", base+"/admin/reviews?state=not-required", admin, nil, 200, nil))
		if queue.Count != 1 || queue.Items[0].ArtifactID != id {
			t.Fatalf("%s: queue = %+v", base, queue)
		}
	}
}

func TestF8ReviewGateOverHTTP(t *testing.T) {
	f := setup(t)
	_, dev := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	base := "/thread-pool/v1"
	// The admin turns the review gate on with a 2-of-2 quorum.
	cfg := f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"requireReview": true, "reviewQuorum": 2}, 200, nil)
	got := decode[struct {
		RequireReview bool `json:"requireReview"`
		ReviewQuorum  int  `json:"reviewQuorum"`
	}](t, cfg)
	if !got.RequireReview || got.ReviewQuorum != 2 {
		t.Fatalf("config = %+v", got)
	}
	// The upload is accepted and waits for the review.
	module := []byte("reviewed module")
	id := verify.SHA256(module)
	view := decode[f8ArtifactView](t, f.request(t, "PUT", base+"/artifacts", dev, module, 201, map[string]string{"X-Artifact-Sha256": id}))
	if view.ReviewState != pool.ReviewPending {
		t.Fatalf("review state = %q, want %q", view.ReviewState, pool.ReviewPending)
	}
	// A job with the pending module is refused.
	q := pool.CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []pool.TaskSpec{{TaskID: "one"}}}
	f.request(t, "POST", base+"/jobs", dev, q, 409, nil)
	// One approval is not the quorum.
	view = decode[f8ArtifactView](t, f.request(t, "POST", base+"/admin/reviews/"+id, admin, map[string]any{"decision": "approve", "reason": "ok"}, 200, nil))
	if view.ReviewState != pool.ReviewPending {
		t.Fatalf("review state after one approval = %q", view.ReviewState)
	}
	// An unknown decision is refused.
	f.request(t, "POST", base+"/admin/reviews/"+id, admin, map[string]any{"decision": "maybe"}, 400, nil)
	// The second distinct reviewer reaches the quorum. The reviewer identity is the
	// authenticated admin user id, so a second admin key is the second reviewer.
	_, admin2 := f.key(t, "admin")
	view = decode[f8ArtifactView](t, f.request(t, "POST", base+"/admin/reviews/"+id, admin2, map[string]any{"decision": "approve", "reason": "approved"}, 200, nil))
	if view.ReviewState != pool.ReviewApproved {
		t.Fatalf("review state after the quorum = %q, want %q", view.ReviewState, pool.ReviewApproved)
	}
	// The approved module runs as a job module.
	f.request(t, "POST", base+"/jobs", dev, q, 201, nil)
	// The review decision is on the security log.
	var audit []store.Record
	if err := f.db.View(context.Background(), func(r store.Reader) error {
		var e error
		audit, e = r.List(store.Audit)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 2 {
		t.Fatalf("security log rows = %d, want 2 review decisions", len(audit))
	}
}

func TestF8ScanConfigRejectsInvalidValues(t *testing.T) {
	f := setup(t)
	_, admin := f.key(t, "admin")
	base := "/thread-pool/v1"
	f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"maxBytes": 0}, 400, nil)
	f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"reviewQuorum": 0}, 400, nil)
	f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"allowedImports": []string{""}}, 400, nil)
	f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"allowedImports": []string{"a*b"}}, 400, nil)
	// A developer may not change the configuration.
	_, dev := f.key(t, "developer")
	f.request(t, "PUT", base+"/admin/upload-scan", dev, map[string]any{"requireReview": false}, 403, nil)
}

func TestF8RescanRoute(t *testing.T) {
	f := setup(t)
	_, dev := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	base := "/thread-pool/v1"
	module := []byte("rescan module")
	id := verify.SHA256(module)
	f.request(t, "PUT", base+"/artifacts", dev, module, 201, map[string]string{"X-Artifact-Sha256": id})
	view := decode[f8ArtifactView](t, f.request(t, "POST", base+"/admin/reviews/"+id+":rescan", admin, nil, 200, nil))
	if view.Scan == nil || !view.Scan.OK {
		t.Fatalf("rescan = %+v", view)
	}
	// An unknown action is a 404.
	f.request(t, "POST", base+"/admin/reviews/"+id+":unknown", admin, nil, 404, nil)
}

func TestF8FailedScanReturnsProblemBody(t *testing.T) {
	f := setup(t)
	_, dev := f.key(t, "developer")
	_, admin := f.key(t, "admin")
	base := "/thread-pool/v1"
	// The allow-list is narrowed so that the WASI import of the fixture is refused.
	f.request(t, "PUT", base+"/admin/upload-scan", admin, map[string]any{"allowedImports": []string{"env.abort"}}, 200, nil)
	// A WASM binary with one import outside the new allow-list.
	module := f8WasmWithImport("wasi_snapshot_preview1", "fd_write")
	id := verify.SHA256(module)
	body := f.request(t, "PUT", base+"/artifacts", dev, module, 422, map[string]string{"X-Artifact-Sha256": id, "Content-Type": "application/wasm"})
	problem := decode[struct {
		Code string `json:"code"`
		Scan *struct {
			OK       bool `json:"ok"`
			Findings []struct {
				Code string `json:"code"`
			} `json:"findings"`
		} `json:"scan"`
	}](t, body)
	if problem.Code != "artifact-rejected" {
		t.Fatalf("problem code = %q", problem.Code)
	}
	if problem.Scan == nil || problem.Scan.OK {
		t.Fatalf("scan in problem body = %+v", problem.Scan)
	}
	// The rejected artifact is on the admin review queue.
	queue := decode[struct {
		Items []f8ArtifactView `json:"items"`
	}](t, f.request(t, "GET", base+"/admin/reviews?state=rejected", admin, nil, 200, nil))
	if len(queue.Items) != 1 || queue.Items[0].ArtifactID != id {
		t.Fatalf("rejected queue = %+v", queue.Items)
	}
}

// f8WasmWithImport builds a minimal WASM binary with one function import.
func f8WasmWithImport(module, field string) []byte {
	leb := func(v uint64) []byte {
		out := []byte{}
		for {
			b := byte(v & 0x7f)
			v >>= 7
			if v != 0 {
				b |= 0x80
			}
			out = append(out, b)
			if v == 0 {
				return out
			}
		}
	}
	name := func(s string) []byte {
		return append(leb(uint64(len(s))), []byte(s)...)
	}
	payload := leb(1)
	payload = append(payload, name(module)...)
	payload = append(payload, name(field)...)
	payload = append(payload, 0x00) // function import
	payload = append(payload, leb(0)...)
	out := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
	out = append(out, 0x02) // import section
	out = append(out, leb(uint64(len(payload)))...)
	return append(out, payload...)
}
