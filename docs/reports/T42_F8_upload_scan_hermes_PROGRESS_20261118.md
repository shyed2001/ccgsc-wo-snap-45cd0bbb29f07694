2026-11-18 F8 START: read WO_T42_FEATURES.md row F8 (upload scan/review). Scope: module upload scan (size, wasm magic, imports allow-list) + admin review state.
2026-11-18 F8 STEP: no prior PROGRESS.log / partial F8 files found; starting fresh.
2026-11-18 F8 STEP: read engine.go, api.go, model.go, resources.go, session.go, store.go, httpx, auth, sqlite/pg adapters; reviewed WWPP-R36.1 (R32.7/R12.1/R13.1/R10.0) in docs/reviews + docs/decisions/GRILL_WWPP_R1.md.
2026-11-18 F8 STEP: baseline `go build ./...` OK (go1.26.2 windows/amd64). Whole work copy is CRLF (pre-existing checkout state; merge_job.py normalizes to LF) - verified my files are gofmt-clean on LF-normalized copies.
2026-11-18 F8 WROTE: internal/pool/upload_scan_F8.go (scan config, WASM parser, import allow-list, shared-memory reject, review gate, quorum, audit row).
2026-11-18 F8 WROTE: internal/pool/upload_scan_F8_test.go (12 tests: import allow-list, shared memory, size ceiling, wasm magic, quorum, pending/rejected blocks job, queue, rescan, durable config, owner auto-approve).
2026-11-18 F8 EDIT: internal/pool/model.go - Artifact gains Scan/ReviewState/ReviewReason/Reviewers/ReviewQuorum/ReviewedAt fields (smallest hook).
2026-11-18 F8 EDIT: internal/pool/resources.go - Upload delegates to new UploadOwned; scan runs on complete bytes; failed scan keeps artifact stored for review evidence.
2026-11-18 F8 EDIT: internal/pool/engine.go - loadUploadScanConfig at New; moduleApproved gate on job create.
2026-11-18 F8 EDIT: internal/pool/session.go - review gate on session modules + warm initializer.
2026-11-18 F8 WROTE: internal/api/upload_scan_F8.go (admin review queue/view/decision/rescan + scan config GET/PUT).
2026-11-18 F8 EDIT: internal/api/api.go - upload route uses UploadOwned and returns scan view; capabilities advertises uploadScan truthfully; F8 admin routes registered.
2026-11-18 F8 TEST: `go test ./internal/pool -run TestF8` OK (12/12 pass).
2026-11-18 F8 WROTE: tests/upload_scan_F8_test.go (4 HTTP tests: scan report over HTTP, review gate quorum + audit rows, invalid config 400 + 403, rescan route).
2026-11-18 F8 TEST: `go build ./...` OK; `go vet ./...` OK; `go test -count=1 ./...` ALL PASS; `go test -count=1 -race ./...` ALL PASS (no races).
