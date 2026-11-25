# FIX-P2

Implemented the FIX-P2 chain and signing-key fixes.

- Historical Merkle checkpoints now verify a recorded tip as a valid prefix of the current append-only chain, while detecting rewritten, missing, or broken chains.
- Task-signing keyring public keys and retirement/revocation state are persisted and restored across engine restarts, including overlap windows.
- Added regressions for valid checkpoint append behavior and retired-key restart behavior.

Validation:

- `gofmt` passed.
- `go vet ./...` passed.
- `go build ./...` passed.
- `go test -race ./... -count=1` passed, including `./tests`.
- Web tests: 189 passed, 1 pre-existing failure in `web/pages/tests/contribute.test.mjs` for `runner.html` generation.
- Graphify code-only update completed; document extraction was skipped because no LLM key is configured.

DONE FIX-P2
