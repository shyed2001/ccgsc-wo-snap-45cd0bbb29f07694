FIX-Q2 completed.

Implemented:
- Session expiry now uses live worker heartbeats; Heartbeat refreshes the durable session tab snapshot.
- Session and session-job indexes rebuild from durable storage during Engine.New.
- Per-owner artifact byte limits are reserved and enforced at upload creation.
- Lease renewals preserve each task's TimeoutMS deadline, including batch members.
- CPU/GPU runtime seconds are recorded on lease settlement and expiry.
- Added focused regression tests in internal/pool/fix_q2_test.go.

Held as already fixed: the warm-initializer approval gate is already outside the module loop in session.go.

Validation:
- go test -count=1 ./...
- go test -count=1 -race ./...
- go vet ./...
- FIX-Q2 files gofmt clean
- web page tests passed
- page JavaScript syntax checks passed
- graphify update completed; SQL extraction warning only (optional tree_sitter_sql dependency absent)

DONE FIX-Q2
