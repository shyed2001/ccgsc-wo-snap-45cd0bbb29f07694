// Path: web/embed.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: three embeds - the site shell, the one shared nav, and the agy page tree that
// pools-core serves under /pools/; WO-T2.2, WWPP-R38.5, R38.5b, R40.1-R40.3.
package web

import (
	"embed"
	"io/fs"
)

// Files is the site shell: the legacy placeholder pages served under /web/.
// The nav assets are listed here too, so the historical /web/nav.js and /web/nav.json
// links keep working; there is only one nav.js and one nav.json on disk.
//
//go:embed admin monitor contribute nav.js nav.json
var Files embed.FS

// Nav is the one shared navigation component, its link registry and its demo page.
// It is served at /nav/ so that every page - the /pools/ pages, the /pools/manual/
// pages and the /web/ placeholders - loads the same nav.js and the same nav.json.
//
//go:embed nav.js nav.json nav_demo.html
var Nav embed.FS

// Pages is the real agy page set: the seven monitor/admin pages, their manual pages,
// the shared stylesheet and the page scripts. It is served under /pools/.
// It carries the platform only: the workloads are embedded separately below, so a
// workload change never rebuilds this tree.
//
// The glob must reach the subdirectories of pages/js as well: runner.js imports
// './platform/hash.js', and a single-level 'pages/js/*.js' left that module out of
// the embed. The browser then answered /pools/js/platform/hash.js with a 404, the
// whole module graph of contribute.js failed to load, and the contributor page
// stayed without the pools runtime.
//
//go:embed pages/*.html pages/manual/*.html pages/css/*.css pages/js/*.js pages/js/**/*.js
var Pages embed.FS

// Workloads is the workload tree: one folder per workload, each with its entry
// module and its manifest.json (id, version, kind, entry, sha256 pin). It is
// embedded apart from Pages on purpose (W10): a workload changes without touching
// the platform page tree, and runner.html is built only from pages/js.
//
// The wasm-abi example (D-W10-10) ships both kernels and its source: the source
// is embedded so the built artifact can always be traced back to it, and the
// kernels are what the seed registers.
//
//go:embed workloads/matmul/matmul_kernel.js workloads/matmul/manifest.json
//go:embed workloads/matmul_wasm/manifest.json workloads/matmul_wasm/matmul.c
//go:embed workloads/matmul_wasm/matmul_scalar.wasm workloads/matmul_wasm/matmul_simd.wasm
var Workloads embed.FS

// NavRoot returns the embedded nav assets rooted at the nav directory.
func NavRoot() (fs.FS, error) { return fs.Sub(Nav, ".") }

// PagesRoot returns the embedded page tree rooted at the pages directory, so that
// /pools/monitor.html maps to pages/monitor.html.
func PagesRoot() (fs.FS, error) { return fs.Sub(Pages, "pages") }

// WorkloadRoot returns the embedded workload tree, so a workload file is read as
// "matmul/matmul_kernel.js".
func WorkloadRoot() (fs.FS, error) { return fs.Sub(Workloads, "workloads") }
