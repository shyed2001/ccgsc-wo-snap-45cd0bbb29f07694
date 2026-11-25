FIX-S4 completed.

Root cause: the PostgreSQL engine integration harness replaced `search_path` with only its
random isolation schema. The production config functions loaded from `db/pool_config.sql`
remain in `public`, so PostgreSQL rejected prepared engine statements such as
`pool_config_lock_page(unknown)`.

Changed `pools-core/internal/pool/pg_engine_integration_test.go` to retain the isolated
schema first while including `public` for the installed config functions. The integration
failure helper now prints the complete PostgreSQL error during failures.

Validation:
- `go build ./...` passed.
- `go test ./...` passed, including `./tests`.
- `go test -race ./...` passed.
- `go vet ./internal/store/pg ./internal/pool` passed.
- Page/navigation tests passed: 146 page tests and 22 navigation tests.
- On `lab-node-02`, PostgreSQL 18.6 with `t51` and `db/pool_config.sql`: all 6 `PGEngine`
  tests passed. PostgreSQL was stopped and temporary lab files were removed.

DONE FIX-S4
