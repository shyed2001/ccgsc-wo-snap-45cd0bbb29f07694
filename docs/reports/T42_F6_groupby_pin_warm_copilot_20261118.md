# T42 F6

## Files changed

- `pools-core/internal/pool/group_pin_F6.go`: validates groupBy/pin requests, filters pinned workers, and groups same-key tasks in a batch.
- `pools-core/internal/pool/group_pin_F6_test.go`: covers same-group batching and pinned warm sessions.
- `pools-core/internal/pool/model.go`: exposes session pin and warm initializer artifact state.
- `pools-core/internal/pool/session.go`: supports pinned session selection and validated warm initializer artifacts.
- `pools-core/internal/pool/batch_F1.go`: applies placement filtering during claims.
- `pools-core/internal/pool/engine.go`: accepts groupBy/pin while retaining unsupported handling for options.
- `pools-core/internal/api/api.go`: reports groupBy, pin, and warm-initializer capabilities.

## Verification

- `gofmt`: PASS
- `go build ./...`: PASS
- `go vet ./...`: PASS
- `go test -count=1 -race ./...`: PASS

No git commands, installs, or servers were used.

DONE T42F6
