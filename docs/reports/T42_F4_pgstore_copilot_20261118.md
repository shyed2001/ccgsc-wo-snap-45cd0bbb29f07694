# T42 F4 report

## Files changed

- `pools-core/internal/store/pg/pg.go` - implemented the pgx-backed `store.Store` with transactional views/updates, row-locking reads in write transactions, JSON record persistence, and a partial unique `(job_id, task_id)` index for result records.
- `pools-core/cmd/pools-core/main.go` - selects PostgreSQL from `POOLS_PG_DSN`; otherwise starts with SQLite.
- `pools-core/tests/core_test.go` - replaced the obsolete PG-stub assertion with empty-DSN validation.
- `pools-core/internal/store/pg/store_test.go` - added DSN-gated CRUD, rollback, and duplicate-result coverage.

## Validation

- `gofmt` passed.
- `go build ./...` passed.
- `go vet ./...` passed.
- `go test -count=1 -race ./...` passed.
- PostgreSQL integration coverage skips when `POOLS_PG_DSN` is unset, as required.

DONE T42F4
