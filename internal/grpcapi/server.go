// Path: internal/grpcapi/server.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the gRPC server of the pools: the interceptor that enforces the
// rate limit with codes.ResourceExhausted and the retry-after trailer, and the
// single-port h2c wiring that serves gRPC and the REST routes side by side;
// WO T42 F10.
package grpcapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// TrailerRetryAfter is the name of the trailer that carries the retry delay of
// a rate limited call. It is the gRPC twin of the Retry-After header of the
// REST surface.
const TrailerRetryAfter = "retry-after"

// TrailerTraceID is the name of the trailer that carries the trace id of the
// call.
const TrailerTraceID = "x-request-id"

// Server is the gRPC server of the pools.
type Server struct {
	grpc *grpc.Server
	// service is the shared service behind the two pool services.
	service *Service
}

// NewServer builds the gRPC server and registers the two pool services.
func NewServer(service *Service, opts ...grpc.ServerOption) *Server {
	opts = append([]grpc.ServerOption{
		grpc.ChainUnaryInterceptor(service.unaryInterceptor),
		grpc.ChainStreamInterceptor(service.streamInterceptor),
	}, opts...)
	server := grpc.NewServer(opts...)
	RegisterThreadPoolServer(server, NewThreadPoolService(service))
	RegisterProcessPoolServer(server, NewProcessPoolService(service))
	return &Server{grpc: server, service: service}
}

// GRPC returns the gRPC server, so a caller can serve it on its own listener.
func (s *Server) GRPC() *grpc.Server { return s.grpc }

// Service returns the shared service of the server.
func (s *Server) Service() *Service { return s.service }

// Serve accepts the connections of the gRPC listener until the listener fails.
func (s *Server) Serve(l net.Listener) error { return s.grpc.Serve(l) }

// Stop stops the gRPC server.
func (s *Server) Stop() { s.grpc.Stop() }

// GracefulStop stops the gRPC server after the pending calls finished.
func (s *Server) GracefulStop() { s.grpc.GracefulStop() }

// Handler returns the http.Handler of the gRPC server, so it can share a port
// with the REST routes. The handler only accepts the gRPC content type.
func (s *Server) Handler() http.Handler { return s.grpc }

// Mux returns the router that serves both transports on one port: the gRPC
// calls go to the gRPC server and every other request goes to the REST
// handler. The h2c upgrade lets a plain HTTP client speak gRPC without TLS,
// which is the mode of the local debug service.
func (s *Server) Mux(rest http.Handler) http.Handler {
	return h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			s.grpc.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	}), &http2.Server{})
}

// unaryInterceptor enforces the rate limit of the server. A rate limited call
// answers with codes.ResourceExhausted, the retry-after trailer and the
// ErrorDetail that names the retry delay, which is the gRPC twin of the 429
// and the Retry-After header of the REST surface.
func (s *Service) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.rateLimit(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// streamInterceptor enforces the rate limit of the server on the streaming
// calls too.
func (s *Service) streamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.rateLimit(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// rateLimit applies the rate limiter of the server to the call.
func (s *Service) rateLimit(ctx context.Context) error {
	if s.RateLimit == nil {
		return nil
	}
	if s.RateLimit.Allow(s.clientIP(ctx)) {
		return nil
	}
	detail := &ErrorDetail{
		Code:              "rate-limit-exceeded",
		Retryable:         true,
		Message:           http.StatusText(http.StatusTooManyRequests),
		Detail:            "Too many requests. Please retry later.",
		Status:            http.StatusTooManyRequests,
		Type:              "about:blank",
		Title:             http.StatusText(http.StatusTooManyRequests),
		RetryAfterSeconds: int64(retryWindow.Seconds()),
	}
	setTrailers(ctx, retryWindow, "")
	withDetails, err := status.New(codes.ResourceExhausted, detail.Message).WithDetails(detail)
	if err != nil {
		return status.Error(codes.ResourceExhausted, detail.Message)
	}
	return withDetails.Err()
}

// clientIP reads the address of the caller of the gRPC call. The gRPC peer
// carries it, so the rate limiter sees the same key as the REST surface.
func (s *Service) clientIP(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err == nil {
			return host
		}
		return p.Addr.String()
	}
	return "unknown"
}

// setTrailers writes the retry-after trailer and the trace id onto the call.
func setTrailers(ctx context.Context, retryAfter time.Duration, trace string) {
	stream := grpc.ServerTransportStreamFromContext(ctx)
	if stream == nil {
		return
	}
	md := metadata.Pairs()
	if retryAfter > 0 {
		md.Set(TrailerRetryAfter, strconv.Itoa(int(retryAfter.Seconds())))
	}
	if trace != "" {
		md.Set(TrailerTraceID, trace)
	}
	stream.SetTrailer(md)
}
