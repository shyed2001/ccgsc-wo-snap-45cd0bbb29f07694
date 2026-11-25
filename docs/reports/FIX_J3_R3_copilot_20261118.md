FIX-J3 completed in F:\tmp\par\fix_J3\pools-core.

Changed files:
- internal/store/pg/pg.go
  - Executes each pool-record DDL statement separately in one transaction.
  - Adds a raw_body bytea column/migration and preserves raw bytes through Get/List.
  - Serializes same-key idempotency admission with a transaction-scoped advisory lock.
  - Uses non-overwriting ON CONFLICT behavior for idempotency records.
- internal/store/pg/store_test.go
  - Adds exact raw JSON byte round-trip coverage.
  - Adds concurrent same-key idempotency admission coverage.

Validation:
- gofmt: passed.
- go test -count=1 ./internal/store/pg: passed; PostgreSQL DSN-gated tests skipped because POOLS_PG_DSN is unset.
- go build ./...: passed.
- go vet ./...: passed.
- go test -count=1 -race ./...: passed; PostgreSQL DSN-gated tests skipped because POOLS_PG_DSN is unset.
- graphify update .: passed.

DONE FIXJ3
