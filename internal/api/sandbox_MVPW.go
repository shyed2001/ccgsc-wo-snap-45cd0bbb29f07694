package api

import (
	"ccgsc/poolscore/internal/httpx"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) egressRunOriginReady() bool {
	run, err := url.Parse(s.Config.RunOrigin)
	if err != nil || run.Host == "" {
		return false
	}
	public, err := url.Parse(s.Config.PublicOrigin)
	return err == nil && public.Host != "" && !strings.EqualFold(run.Host, public.Host)
}

func (s *Server) isRunOriginRequest(r *http.Request) bool {
	if s.Config.RunOrigin == "" {
		return false
	}
	u, err := url.Parse(s.Config.RunOrigin)
	return err == nil && u.Host != "" && strings.EqualFold(r.Host, u.Host)
}

func (s *Server) serveRunOrigin(pagesRoot fs.FS, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	frame := "'self'"
	if s.Config.PublicOrigin != "" {
		frame = strings.TrimRight(s.Config.PublicOrigin, "/")
	}
	connect := "'none'"
	if r.URL.Query().Has("grant") {
		if s.Engine == nil {
			http.NotFound(w, r)
			return
		}
		policy, err := s.Engine.RunnerAppPolicy(r.Context(), r.URL.Query().Get("grant"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		connect = policy.ConnectSrc()
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' 'wasm-unsafe-eval' blob:; worker-src 'self' blob:; connect-src "+connect+"; frame-ancestors "+frame)
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Del("Set-Cookie")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/runner.html" {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/", http.FileServer(http.FS(pagesRoot))).ServeHTTP(w, r)
}

func (s *Server) runnerConfig(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"anonymousJoin": s.Config.AnonymousContributors.Enabled,
		// D-W10-10: the page-config bound on a wasm-abi module's linear memory,
		// in 64 KiB pages. The signed envelope still wins; this is only the
		// deployment default, and a job can neither raise nor bypass the signed
		// value of its own envelope.
		"wasmMaxMemoryPages": wasmMaxMemoryPagesDefault,
	}
	if s.Config.RunOrigin != "" {
		resp["runOrigin"] = strings.TrimRight(s.Config.RunOrigin, "/")
	} else {
		resp["runOrigin"] = nil
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// wasmMaxMemoryPagesDefault is the platform default of D-W10-10: 4096 pages =
// 256 MiB. It must stay equal to DEFAULT_WASM_MAX_MEMORY_PAGES in runner.js.
const wasmMaxMemoryPagesDefault = 4096

func runOriginFrameSource(runOrigin string) string {
	if runOrigin == "" {
		return ""
	}
	return strings.TrimRight(runOrigin, "/")
}

func mainContentSecurityPolicy(runOrigin string) string {
	if frame := runOriginFrameSource(runOrigin); frame != "" {
		return "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; worker-src 'self' blob:; frame-src " + frame + "; frame-ancestors 'none'"
	}
	return "frame-ancestors 'none'"
}
