# T42 F7 Report

## Files changed
- `pools-core/internal/api/api.go`: implemented `results:bulk` cursor paging, webhook job-create validation, completion-event dispatcher, and truthful capabilities.
- `pools-core/internal/api/results_F7_test.go`: cursor/limit/failed-result paging coverage.
- `pools-core/internal/config/config.go`: added `webhookAllowPrivateIPs` admin setting.
- `pools-core/internal/pool/model.go`: added persisted webhook URL/secret contract.
- `pools-core/internal/pool/engine.go`: validates webhook shape before job creation.
- `pools-core/internal/pool/webhook_F7.go`: HMAC-SHA256 signing, SSRF validation, redirect blocking, and three-attempt exponential backoff delivery.
- `pools-core/internal/pool/webhook_F7_test.go`: signature, bounded retry, and private-address guard coverage.

## Behavior
- Job completion sends a JSON `job.done` payload with `X-Webhook-Signature: sha256=<hex>` (plus compatibility signature headers).
- Retries are bounded to three attempts with 100ms/200ms backoff.
- DNS-resolved private, loopback, link-local, unspecified, and multicast targets are rejected unless `webhookAllowPrivateIPs` is enabled; redirects are blocked.
- `results:bulk` now supports the same bounded `limit`, cursor, and `includeFailed` paging contract as results.

## Validation
- `gofmt`: passed
- `go build ./...`: passed
- `go vet ./...`: passed
- `go test -count=1 -race ./...`: passed

DONE T42F7
