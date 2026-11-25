FIX-R3 completed.

Changed:
- `pools-core/internal/api/api.go`: IP allowlisting now satisfies only admin routes whose role and scope authorize the synthetic admin claim; developer and worker routes continue through normal session/bearer authorization.
- `pools-core/tests/admin_auth_test.go`: added regression coverage proving allowlisted IPs can access admin pages but cannot bypass developer/worker route authentication.

Validation:
- `go test -race -count=1 ./internal/api ./tests`
- `go vet ./internal/api ./tests`
- `gofmt` applied to edited Go files.
- `graphify update .` completed: 3,781 nodes, 7,681 edges, 323 communities.

Tier 0:
- `cavemem` was available; its CLI help was returned, but no prior-work query was needed.
- GitAutoFleet inspect and subscription watch were skipped because the environment denied permission.
- Graphify update ran successfully; SQL extraction warning is unrelated to FIX-R3.

DONE FIX-R3
