# FIX-K second-pass report

Updated the legacy `tests` expectations to the FIX-K contract without weakening behavioral assertions:

- Complete artifact uploads now expect `201`; incomplete uploads continue to validate `200` and `Upload-Offset`.
- F5 policy checks now read `rerun.defaultPercent`, `rerun.maxPercent`, `serverLimits.defaultBatchSize.{cpu,gpu}`, and `serverLimits.maxBatchSize`.
- F1 capability checks now use `serverLimits`, `gpu`, and top-level `priorityClasses`; split execution remains validated end-to-end.
- F8 checks now read `uploadChecks` and `serverLimits.maxArtifactBytes` instead of removed `uploadScan`.
- Chain tests now follow the new capability/result contract and retain clean-chain and tamper-detection assertions.
- Verified `internal/api/admin_config_F5.go` uses only current capability keys; no production consumer still reads removed `batching` or `uploadScan` fields.

Changed tests:

- `tests/admin_config_F5_test.go`: `TestF5AdminRoutes`
- `tests/split_batch_F1_test.go`: `TestF1HTTPSplitBatchCapabilities`
- `tests/upload_scan_F8_test.go`: `TestF8UploadScanReportsFindingsOverHTTP`, `TestF8ReviewGateOverHTTP`, `TestF8RescanRoute`
- `tests/review_go_test.go`: `TestReviewStandardUploadAndAdminRead`
- `tests/core_test.go`: `TestEndToEndMapThreeTasks`
- `tests/chain_test.go`: `TestCapabilitiesReportTheAuditChain`, `TestChainVerificationIsIndependentOfTheServer`

Validation passed: `go test -count=1 -race ./...`, `gofmt`, and the post-change stale-key scan. Graphify was refreshed; it reported only the pre-existing missing optional `tree_sitter_sql` dependency for three SQL files.

DONE FIXK
