// Path: internal/grpcapi/auth.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the gRPC transport of the shared auth claims, so the gRPC surface
// enforces exactly the same roles and scopes as the HTTP surface of
// internal/api/api.go; WO T42 F10.
package grpcapi

import (
	"context"
	"net/http"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// metadataAuthorization is the metadata key that carries the bearer token. The
// key is the lower case form of the HTTP header, which is how gRPC metadata
// names its keys.
const metadataAuthorization = "authorization"

// metadataFromIncoming reads the metadata of the call.
func metadataFromIncoming(ctx context.Context) (metadata.MD, bool) {
	return metadata.FromIncomingContext(ctx)
}

// ctxOf reads the context of a server stream.
func ctxOf(stream interface{ Context() context.Context }) context.Context {
	return stream.Context()
}

// claimsFromMetadata reads and validates a bearer token from request metadata.
func claimsFromMetadata(ctx context.Context, a *auth.Service, limiter *auth.RateLimiter) (auth.Claims, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return auth.Claims{}, authStatus(codes.Unauthenticated, "missing-authorization", "missing authorization metadata")
	}
	values := md.Get(metadataAuthorization)
	if len(values) == 0 {
		return auth.Claims{}, authStatus(codes.Unauthenticated, "missing-authorization", "missing authorization metadata")
	}
	raw := strings.TrimSpace(values[0])
	if !strings.HasPrefix(raw, "Bearer ") {
		return auth.Claims{}, authStatus(codes.Unauthenticated, "invalid-authorization", "authorization must use the Bearer scheme")
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	if raw == "" {
		return auth.Claims{}, authStatus(codes.Unauthenticated, "missing-authorization", "missing bearer token")
	}
	claims, err := a.Validate(ctx, raw)
	if err != nil {
		return auth.Claims{}, authStatus(codes.Unauthenticated, "unauthorized", "invalid or revoked credentials")
	}
	if limiter != nil && !limiter.Allow(claims.KeyID) {
		setTrailers(ctx, retryWindow, "")
		return auth.Claims{}, authStatus(codes.ResourceExhausted, "rate-limit-exceeded", "Too many requests. Please retry later.")
	}
	return claims, nil
}

func authStatus(code codes.Code, detailCode, message string) error {
	detail := &ErrorDetail{
		Code:      detailCode,
		Message:   message,
		Detail:    message,
		Status:    int32(httpStatusForGRPC(code)),
		Type:      "about:blank",
		Title:     http.StatusText(httpStatusForGRPC(code)),
		Retryable: code == codes.ResourceExhausted,
	}
	withDetails, err := status.New(code, message).WithDetails(detail)
	if err != nil {
		return status.Error(code, message)
	}
	return withDetails.Err()
}

func httpStatusForGRPC(code codes.Code) int {
	switch code {
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

// authorize applies the same role and scope rule as the HTTP surface. The admin
// role is satisfied by admin or director, exactly as in Server.secure.
func authorize(c auth.Claims, role, scope string) error {
	if role == "admin" {
		if (!c.HasRole("admin") && !c.HasRole("director")) || !c.Allows(scope) {
			return status.Error(codes.PermissionDenied, "forbidden")
		}
		return nil
	}
	if !c.HasRole(role) || !c.Allows(scope) {
		return status.Error(codes.PermissionDenied, "forbidden")
	}
	return nil
}

// authorizeArtifactRead mirrors the rule of the HTTP route
// GET /{pool}/v1/artifacts/{artifactId}: a developer or admin with
// artifacts:read, or a contributor with the worker scope.
func authorizeArtifactRead(c auth.Claims) error {
	if (c.HasRole("developer") || c.HasRole("admin")) && c.Allows("artifacts:read") {
		return nil
	}
	if c.HasRole("contributor") && c.Allows("worker") {
		return nil
	}
	return status.Error(codes.PermissionDenied, "forbidden")
}
