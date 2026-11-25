// Path: internal/pool/upload_scan_F8_test.go | Author: hermes (MiniMax-M3) for the owner | 2026-11-18
// What/why: T42 F8 tests of the module upload scan (size, WASM magic, import
// allow-list, shared memory) and of the admin review gate (quorum, states, gate).
package pool

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
)

func nestedZip(depth int) []byte {
	data := []byte("leaf")
	for i := 0; i < depth; i++ {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		f, _ := w.Create("nested.zip")
		_, _ = f.Write(data)
		_ = w.Close()
		data = buf.Bytes()
	}
	return data
}

func newF8Engine(t *testing.T) (*Engine, func()) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "f8", time.Minute)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return e, func() { _ = db.Close() }
}

// wasmBuilder builds a minimal WASM binary for the scan tests.
type wasmBuilder struct {
	imports []struct{ module, field string }
	shared  bool
}

func (b wasmBuilder) build() []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
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
	if len(b.imports) > 0 {
		payload := leb(uint64(len(b.imports)))
		for _, imp := range b.imports {
			payload = append(payload, name(imp.module)...)
			payload = append(payload, name(imp.field)...)
			payload = append(payload, 0x00) // function import
			payload = append(payload, leb(0)...)
		}
		section := append([]byte{0x02}, leb(uint64(len(payload)))...)
		out = append(out, section...)
		out = append(out, payload...)
	}
	if b.shared {
		// Memory section with the shared flag (limits flags 0x03).
		payload := append(leb(1), 0x03)
		payload = append(payload, leb(1)...)
		payload = append(payload, leb(1)...)
		section := append([]byte{0x05}, leb(uint64(len(payload)))...)
		out = append(out, section...)
		out = append(out, payload...)
	}
	return out
}

func TestF8ScanRejectsImportOutsideAllowList(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	// "evil.host" is not on the default allow-list.
	module := wasmBuilder{imports: []struct{ module, field string }{{"evil.host", "exfiltrate"}}}.build()
	id := verify.SHA256(module)
	_, err := e.Upload(ctx, id, "application/wasm", 0, int64(len(module)), module)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("forbidden import accepted: %v", err)
	}
	// The artifact stays stored with its scan report as the review evidence.
	a, err := e.Artifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Scan == nil || a.Scan.OK {
		t.Fatalf("scan report missing or ok: %+v", a.Scan)
	}
	if a.ReviewState != ReviewRejected {
		t.Fatalf("review state = %q, want %q", a.ReviewState, ReviewRejected)
	}
	found := false
	for _, f := range a.Scan.Findings {
		if f.Code == FindingImportNotAllowed {
			found = true
		}
	}
	if !found {
		t.Fatalf("import finding missing: %+v", a.Scan.Findings)
	}
}

func TestF8ScanAllowsListedImport(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	module := wasmBuilder{imports: []struct{ module, field string }{{"env", "abort"}}}.build()
	id := verify.SHA256(module)
	a, err := e.Upload(ctx, id, "application/wasm", 0, int64(len(module)), module)
	if err != nil {
		t.Fatal(err)
	}
	if a.Scan == nil || !a.Scan.OK {
		t.Fatalf("scan failed: %+v", a.Scan)
	}
	if len(a.Scan.Imports) != 1 || a.Scan.Imports[0] != "env.abort" {
		t.Fatalf("imports = %v", a.Scan.Imports)
	}
	if a.ReviewState != ReviewNotRequired {
		t.Fatalf("review state = %q, want %q", a.ReviewState, ReviewNotRequired)
	}
}

func TestF8ScanRejectsSharedMemory(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	module := wasmBuilder{shared: true}.build()
	id := verify.SHA256(module)
	_, err := e.Upload(ctx, id, "application/wasm", 0, int64(len(module)), module)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("shared memory accepted: %v", err)
	}
	a, err := e.Artifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range a.Scan.Findings {
		if f.Code == FindingSharedMemory {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared memory finding missing: %+v", a.Scan.Findings)
	}
}

func TestF8ScanRejectsSizeAboveCeiling(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	cfg := e.UploadScanConfig()
	cfg.MaxBytes = 4
	if err := e.SetUploadScanConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	module := []byte("too large for the ceiling")
	id := verify.SHA256(module)
	_, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(module)), module)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized module accepted: %v", err)
	}
	a, err := e.Artifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range a.Scan.Findings {
		if f.Code == FindingSizeExceeded {
			found = true
		}
	}

	if !found {
		t.Fatalf("size finding missing: %+v", a.Scan.Findings)
	}
}

func TestF8ScanBoundsNestedZipDepth(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	cfg := e.UploadScanConfig()
	report := e.scanBytes(cfg, Artifact{ArtifactID: "nested", ContentType: "application/zip", Data: nestedZip(maxNestedZipDepth + 2)})
	found := false
	for _, finding := range report.Findings {
		if finding.Code == FindingZipEntry && finding.Severity == SeverityError {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("nested ZIP depth was not bounded: %+v", report.Findings)
	}
}

func TestF8ScanWasmMagicMissing(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	module := []byte("not a wasm binary")
	id := verify.SHA256(module)
	a, err := e.Upload(ctx, id, "application/wasm", 0, int64(len(module)), module)
	if !errors.Is(err, ErrArtifactRejected) {
		t.Fatalf("missing magic must reject the upload: %v", err)
	}
	found := false
	for _, f := range a.Scan.Findings {
		if f.Code == FindingWasmMagic && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("magic warning missing: %+v", a.Scan.Findings)
	}
}

func TestF8ReviewGateQuorum(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	cfg := e.UploadScanConfig()
	cfg.RequireReview = true
	cfg.ReviewQuorum = 2
	if err := e.SetUploadScanConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	module := []byte("reviewed module")
	id := verify.SHA256(module)
	a, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(module)), module)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewPending {
		t.Fatalf("review state = %q, want %q", a.ReviewState, ReviewPending)
	}
	// One approval is not enough for a 2-of-2 quorum.
	a, err = e.ReviewArtifact(ctx, id, "reviewer-1", ReviewDecisionApprove, "looks fine")
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewPending {
		t.Fatalf("review state = %q after one approval, want %q", a.ReviewState, ReviewPending)
	}
	// The same reviewer does not count twice.
	a, err = e.ReviewArtifact(ctx, id, "reviewer-1", ReviewDecisionApprove, "still fine")
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewPending {
		t.Fatalf("review state = %q after a repeated approval, want %q", a.ReviewState, ReviewPending)
	}
	// A second distinct reviewer reaches the quorum.
	a, err = e.ReviewArtifact(ctx, id, "reviewer-2", ReviewDecisionApprove, "approved")
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewApproved {
		t.Fatalf("review state = %q, want %q", a.ReviewState, ReviewApproved)
	}
	// The approved module may run as a job module.
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "one"}}}
	if _, err = e.Create(ctx, Thread, "developer", "", q); err != nil {
		t.Fatal(err)
	}
}

func TestF8PendingModuleBlocksJob(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	cfg := e.UploadScanConfig()
	cfg.RequireReview = true
	if err := e.SetUploadScanConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	module := []byte("gated module")
	id := verify.SHA256(module)
	if _, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(module)), module); err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "one"}}}
	if _, err := e.Create(ctx, Thread, "developer", "", q); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending module accepted: %v", err)
	}
	// The rejection is final for the job until an approval arrives.
	if _, err := e.ReviewArtifact(ctx, id, "reviewer-1", ReviewDecisionReject, "not acceptable"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Create(ctx, Thread, "developer", "", q); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejected module accepted: %v", err)
	}
}

func TestF8ReviewQueueAndRescan(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	module := []byte("queue module")
	id := verify.SHA256(module)
	if _, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(module)), module); err != nil {
		t.Fatal(err)
	}
	items, err := e.ReviewQueue(ctx, ReviewNotRequired, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ArtifactID != id {
		t.Fatalf("queue = %+v", items)
	}
	if items[0].Data != nil {
		t.Fatal("queue leaked module bytes")
	}
	// A rescan of an approved artifact keeps the approval.
	if _, err = e.ReviewArtifact(ctx, id, "reviewer-1", ReviewDecisionApprove, "ok"); err != nil {
		t.Fatal(err)
	}
	a, err := e.RescanArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewApproved {
		t.Fatalf("review state after rescan = %q, want %q", a.ReviewState, ReviewApproved)
	}
	if _, err = e.ReviewQueue(ctx, "unknown-state", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state accepted: %v", err)
	}
}

func TestF8ScanConfigDurableAcrossEngines(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	cfg := e.UploadScanConfig()
	cfg.RequireReview = true
	cfg.ReviewQuorum = 2
	cfg.AllowedImports = []string{"env.log"}
	if err := e.SetUploadScanConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(e.Store, e.Log, "f8b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.UploadScanConfig()
	if !got.RequireReview || got.ReviewQuorum != 2 || len(got.AllowedImports) != 1 {
		t.Fatalf("reloaded config = %+v", got)
	}
	// An invalid configuration is refused.
	bad := got
	bad.ReviewQuorum = 0
	bad.MaxBytes = -1
	if err = reloaded.SetUploadScanConfig(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid config accepted: %v", err)
	}
}

func TestF8OwnerCodeIsAutoApproved(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	module := []byte("owner module")
	id := verify.SHA256(module)
	a, err := e.UploadOwned(ctx, id, "application/octet-stream", 0, int64(len(module)), module, "owner-key", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReviewState != ReviewApproved {
		t.Fatalf("review state = %q, want %q", a.ReviewState, ReviewApproved)
	}
	if a.Owner != "owner-key" {
		t.Fatalf("owner = %q", a.Owner)
	}
}

func TestF8OwnerCodeDoesNotApproveIncompleteArtifact(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	ctx := context.Background()
	data := []byte("partial owner module")
	id := verify.SHA256(data)
	a, err := e.UploadOwned(ctx, id, "application/octet-stream", 0, int64(len(data)+1), data, "owner-key", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Complete {
		t.Fatal("partial upload unexpectedly complete")
	}
	if a.ReviewState == ReviewApproved || a.ReviewedAt != nil {
		t.Fatalf("incomplete artifact was approved: %+v", a)
	}
}

func TestF8ZipWASMUsesModulePolicies(t *testing.T) {
	e, done := newF8Engine(t)
	defer done()
	cfg := e.UploadScanConfig()
	cfg.AllowedImports = []string{"env.abort"}
	cfg.MaxWASMPages = 1
	module := wasmBuilder{imports: []struct{ module, field string }{{"evil.host", "exfiltrate"}}}.build()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create("module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(module); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	report := e.scanBytes(cfg, Artifact{ArtifactID: "zip", Data: buf.Bytes(), ContentType: "application/zip"})
	for _, code := range []string{FindingImportNotAllowed} {
		found := false
		for _, finding := range report.Findings {
			if finding.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ZIP scan missing %s finding: %+v", code, report.Findings)
		}
	}
}

func TestF8ParseWASMReadsImportSection(t *testing.T) {
	module := wasmBuilder{imports: []struct{ module, field string }{{"env", "log"}, {"wasi_snapshot_preview1", "fd_write"}}}.build()
	parsed, err := parseWASM(module)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.imports) != 2 || parsed.imports[0] != "env.log" || parsed.imports[1] != "wasi_snapshot_preview1.fd_write" {
		t.Fatalf("imports = %v", parsed.imports)
	}
	if parsed.shared {
		t.Fatal("shared memory reported for a private module")
	}
	// A truncated binary is malformed, not a crash.
	if _, err = parseWASM(module[:len(module)-3]); err == nil {
		t.Fatal("truncated binary accepted")
	}
}

// The wasmBuilder output must stay a valid binary for the parser above.
func TestF8WasmBuilderShape(t *testing.T) {
	module := wasmBuilder{imports: []struct{ module, field string }{{"env", "log"}}}.build()
	if len(module) < 8 || binary.LittleEndian.Uint32(module[4:8]) != 1 {
		t.Fatalf("binary header = %v", module)
	}
}
