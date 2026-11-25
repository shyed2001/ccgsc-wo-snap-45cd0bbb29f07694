// Path: internal/profiling/profiling.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-1 (GM2 row 1.1) - the optional pprof endpoint of the server.
// It is off by default and it may only listen on a loopback address: a CPU profile of
// the server is a map of the running process and must never reach the public internet.
package profiling

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// ValidateAddr refuses every address that is not a loopback address. The rules:
//   - the address must carry a port,
//   - the host must be a loopback IP (127.0.0.0/8 or ::1) or a name that resolves to
//     loopback addresses only,
//   - a wildcard host (0.0.0.0, ::, empty) is refused.
func ValidateAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("the pprof address is empty, a loopback address like 127.0.0.1:6060 is required")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("the pprof address %q is not host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("the pprof address %q carries no port", addr)
	}
	if host == "" {
		return fmt.Errorf("the pprof address %q must name a loopback host, a wildcard host is refused", addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return fmt.Errorf("the pprof address %q is not a loopback address, only 127.0.0.1 or ::1 are allowed", addr)
		}
		return nil
	}
	// A name is only allowed when every address it resolves to is a loopback address.
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return fmt.Errorf("the pprof host %q does not resolve: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("the pprof host %q does not resolve to any address", host)
	}
	for _, a := range addrs {
		if !a.IP.IsLoopback() {
			return fmt.Errorf("the pprof host %q resolves to %s, which is not a loopback address", host, a.IP)
		}
	}
	return nil
}

// EnableRuntimeProfiles turns the mutex and the block profiling on. The Go runtime
// keeps them off by default, so without these two calls the /debug/pprof/mutex and
// /debug/pprof/block endpoints would always answer an empty profile.
func EnableRuntimeProfiles() {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)
}

// Handler returns the mux with the net/http/pprof endpoints: the index, the CPU
// profile, the heap, the goroutine, the mutex and the block profile, the command line
// and the trace.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// The mutex and the block profile are named profiles of the index handler.
	mux.HandleFunc("/debug/pprof/mutex", pprof.Index)
	mux.HandleFunc("/debug/pprof/block", pprof.Index)
	return mux
}

// Server owns the pprof listener of one running server.
type Server struct {
	http *http.Server
	addr string
}

// Start starts the pprof endpoint on addr. The address must pass ValidateAddr
// before; Start refuses it again, so a caller cannot skip the check.
func Start(addr string, log *slog.Logger) (*Server, error) {
	if err := ValidateAddr(addr); err != nil {
		return nil, err
	}
	EnableRuntimeProfiles()
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on the pprof address %q: %w", addr, err)
	}
	s := &Server{http: srv, addr: ln.Addr().String()}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			if log != nil {
				log.Error("pprof.serve", "addr", s.addr, "error", err.Error())
			}
		}
	}()
	if log != nil {
		log.Info("pprof.start", "addr", s.addr)
	}
	return s, nil
}

// Addr returns the address the pprof endpoint really listens on.
func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

// Close stops the pprof endpoint.
func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}
