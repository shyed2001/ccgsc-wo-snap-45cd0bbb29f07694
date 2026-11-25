// Path: internal/grpcapi/client_context.go | Author: Copilot | Date: 2026-11-20
// What/why: Shared bearer metadata helper for callers of the generated clients.
package grpcapi

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// WithBearer attaches the bearer token to the outgoing gRPC metadata. An
// authorization value already on the context is replaced, never stacked
// (T102): the server reads the first metadata value, so a stale token
// appended earlier would otherwise shadow the fresh one on a reconnect.
func WithBearer(ctx context.Context, token string) context.Context {
	if strings.TrimSpace(token) == "" {
		return ctx
	}
	fresh := "Bearer " + token
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return metadata.AppendToOutgoingContext(ctx, metadataAuthorization, fresh)
	}
	if vals := md.Get(metadataAuthorization); len(vals) == 1 && vals[0] == fresh {
		return ctx
	}
	next := md.Copy()
	for k := range next {
		if strings.EqualFold(k, metadataAuthorization) {
			delete(next, k)
		}
	}
	next[metadataAuthorization] = []string{fresh}
	return metadata.NewOutgoingContext(ctx, next)
}
