// Path: internal/grpcapi/errors.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: one mapping from the engine errors to the gRPC status codes and to
// the ErrorDetail that the REST problem body carries, so both transports report
// the same machine-readable code; WO T42 F10.
package grpcapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorCodes maps the HTTP status of the REST surface to the gRPC code. The
// REST surface owns the truth of the status; the gRPC surface only translates.
var errorCodes = map[int]codes.Code{
	http.StatusBadRequest:            codes.InvalidArgument,
	http.StatusUnauthorized:          codes.Unauthenticated,
	http.StatusForbidden:             codes.PermissionDenied,
	http.StatusNotFound:              codes.NotFound,
	http.StatusConflict:              codes.Aborted,
	http.StatusRequestEntityTooLarge: codes.ResourceExhausted,
	http.StatusUnprocessableEntity:   codes.FailedPrecondition,
	http.StatusTooManyRequests:       codes.ResourceExhausted,
	http.StatusNotImplemented:        codes.Unimplemented,
	http.StatusServiceUnavailable:    codes.Unavailable,
	http.StatusInternalServerError:   codes.Internal,
}

// errorCodeNames is the fallback code of one status, used when a caller only knows the
// status and not the machine-readable code of the REST surface.
var errorCodeNames = map[int]string{
	http.StatusBadRequest:            "invalid-request",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusForbidden:             "forbidden",
	http.StatusNotFound:              "not-found",
	http.StatusConflict:              "state-conflict",
	http.StatusRequestEntityTooLarge: "artifact-too-large",
	http.StatusUnprocessableEntity:   "artifact-rejected",
	http.StatusTooManyRequests:       "quota-exceeded",
	http.StatusNotImplemented:        "not-implemented",
	http.StatusServiceUnavailable:    "service-unavailable",
	http.StatusInternalServerError:   "internal-error",
}

// statusFor maps an engine error to the HTTP status AND the machine-readable code of
// the REST surface. The code is the same one httpx.Error emits, so the hint and the
// retryable field of the gRPC detail answer exactly like the REST problem body.
// R18B review row 13: a collapsed per-status name answered every 409 with
// "state-conflict" (not retryable), although the REST surface answers an empty pool
// with the retryable "capacity-unavailable".
func statusFor(err error) (int, string, error) {
	if err == nil {
		return http.StatusOK, "", nil
	}
	var quota *pool.QuotaError
	switch {
	case errors.Is(err, pool.ErrInvalid):
		return http.StatusBadRequest, "invalid-request", err
	case errors.Is(err, pool.ErrRunnerStale):
		return http.StatusConflict, "runner-stale", err
	case errors.Is(err, pool.ErrConflict):
		return http.StatusConflict, "state-conflict", err
	case errors.As(err, &quota):
		return http.StatusTooManyRequests, "quota-exceeded", err
	case errors.Is(err, pool.ErrForbidden):
		return http.StatusForbidden, "forbidden", err
	case errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized", err
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not-found", err
	case errors.Is(err, pool.ErrUnsupported), errors.Is(err, store.ErrNotImplemented):
		return http.StatusNotImplemented, "not-implemented", err
	case errors.Is(err, pool.ErrEmpty):
		return http.StatusConflict, "capacity-unavailable", err
	}
	return http.StatusInternalServerError, "internal-error", err
}

// errorDetailFor builds the ErrorDetail of one HTTP-equivalent status and cause. The
// legacy fields keep their meaning (R18B review row 12): message is the HTTP status
// text, exactly as it shipped before WO R17-ENVELOPE, and the detail carries the
// sanitized cause - a 500 detail is generic, so SQL, paths and other internals never
// leak to the client.
func errorDetailFor(httpStatus int, code string, cause error, requestID string) (*ErrorDetail, codes.Code) {
	grpcCode, ok := errorCodes[httpStatus]
	if !ok {
		grpcCode = codes.Internal
	}
	if code == "" {
		code, ok = errorCodeNames[httpStatus]
		if !ok {
			code = "internal-error"
		}
	}
	detail := &ErrorDetail{
		Code:      code,
		TraceId:   traceID(cause),
		RequestId: requestID,
		Retryable: httpx.RetryableFor(code, httpStatus),
		Message:   http.StatusText(httpStatus),
		Hint:      httpx.HintFor(code, httpStatus),
		Detail:    detailTextFor(httpStatus, cause),
		Status:    int32(httpStatus),
		Type:      "about:blank",
		Title:     http.StatusText(httpStatus),
	}
	if httpStatus == http.StatusTooManyRequests {
		detail.RetryAfterSeconds = int64(retryAfter(cause).Seconds())
	}
	return detail, grpcCode
}

// detailTextFor is the sanitized detail of a failure: the cause itself for every
// client-facing status, and a fixed generic text for an internal error, so a store or
// network message never leaks (R18B review row 12). The explicit texts of the stream
// error frames are server-authored and safe; only the arbitrary causes of a 500 are
// replaced.
func detailTextFor(httpStatus int, cause error) string {
	if httpStatus >= http.StatusInternalServerError { // N13: never leak arbitrary server-side 5xx causes
		return "The operation could not be completed."
	}
	return cause.Error()
}

// detailStatusFor turns one HTTP-equivalent status, code and cause into the gRPC
// status that carries the ErrorDetail of the REST problem body.
func detailStatusFor(httpStatus int, code string, cause error, requestID string) error {
	detail, grpcCode := errorDetailFor(httpStatus, code, cause, requestID)
	// The detail travels in the google.rpc.Status details, the gRPC equivalent
	// of the REST problem body.
	withDetails, derr := status.New(grpcCode, detail.Message).WithDetails(detail)
	if derr != nil {
		return status.Error(grpcCode, detail.Message)
	}
	return withDetails.Err()
}

// toStatus converts an engine error into a gRPC status that carries the
// ErrorDetail of the REST problem body. The detail always names the same
// machine-readable code as the REST surface, and the request id comes from the
// x-request-id metadata of the call - never from a freshly invented random
// value (R18B review row 16).
func toStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
		// A status that this package built already carries its detail.
		return err
	}
	httpStatus, code, cause := statusFor(err)
	return detailStatusFor(httpStatus, code, cause, requestIDFromMetadata(ctx))
}

// metadataRequestID is the metadata key that carries the caller's request id.
const metadataRequestID = "x-request-id"

// requestIDFromMetadata reads the request id the caller sent in the metadata of the
// call; it is empty when the caller sent none (R18B review row 16).
func requestIDFromMetadata(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	md, ok := metadataFromIncoming(ctx)
	if !ok {
		return ""
	}
	values := md.Get(metadataRequestID)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// retryAfter reads the retry delay of a quota failure. The engine reports it in
// the quota error; every other failure has no retry delay.
func retryAfter(err error) time.Duration {
	var quota *pool.QuotaError
	if errors.As(err, &quota) {
		return quota.RetryAfter
	}
	return 0
}

// traceID reads the trace id that the engine attached to the error, if any.
// The engine does not attach one today, so the detail keeps an empty trace id
// rather than inventing one.
func traceID(err error) string {
	type tracer interface{ TraceID() string }
	var t tracer
	if errors.As(err, &t) {
		return t.TraceID()
	}
	return ""
}

// notImplemented builds the Unimplemented status that the reserved features
// answer with. The REST surface answers them with the 501 problem body, so the
// gRPC surface answers with the same machine-readable code.
func notImplemented(detail string) error {
	return toStatus(context.Background(), pool.ErrUnsupported)
}
