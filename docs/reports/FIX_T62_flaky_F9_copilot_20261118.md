# FIXT62

Changed `pools-core/internal/pool/engine.go`: late accepted submissions no longer enter the probabilistic re-run path. They are settled directly, preserving the required `ExpiredLease.Consumed` and `Job.Results` state atomically.

The failure was not a data race, maintenance goroutine, clock issue, or store save-ordering bug. Under the default 1% re-run sampling, an F9 late result could be held as `Task.Pending` instead of being appended to `Job.Results`, producing the observed state.

Validation:
- `go test -race -count=30 ./internal/pool/ -run TestF9` — passed
- `GOMAXPROCS=2 go test -race -count=5 ./internal/pool/` — passed
- `go test -race ./...` — passed
DONE FIXT62
