FIX-S3 completed.

Changed trust handling to:
- bind persisted canary evidence to the worker and canary lease;
- atomically read-or-create task-signing and re-run sampling keys, including SQLite/PostgreSQL insert-if-absent storage;
- record inconclusive re-run expiry/attempt exhaustion without penalizing the first worker;
- key canary fixture selection and verifier-facade re-run sampling.

Regression coverage includes cross-worker canary inheritance, concurrent engine startup key reuse, and neutral inconclusive reputation events.

Validation passed:
- `go build ./...`
- `go vet ./...`
- `go test -count=1 ./...`
- `go test -race -count=1 ./...`

Tier 0: repository brief ran; cavemem/subs_watch were unavailable. Graphify update passed; it reported three SQL files skipped because `tree_sitter_sql` is unavailable. No installs, git mutations, or unrelated work were performed.
DONE FIX-S3
