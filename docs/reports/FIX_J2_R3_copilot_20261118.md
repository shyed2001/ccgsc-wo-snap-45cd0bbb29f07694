FIX-J2 completed.

Changed:
- `pools-core/internal/pool/scheduling.go`: added verification-task detection, reserved-lane ordering, claim-time running-job quota enforcement, and verification quota bypass.
- `pools-core/internal/pool/engine.go`: applies reserved verification scheduling and isolates verification claims to single-task batches.
- `pools-core/internal/pool/scheduling_test.go`: added focused quota and verification-priority tests.

Verification:
- `gofmt` check passed.
- `go test -count=1 ./internal/pool` passed.
- `go build ./...` passed.
- `go vet ./...` passed.
- `go test -count=1 -race ./...` passed.
- `graphify update .` completed.

DONE FIXJ2
