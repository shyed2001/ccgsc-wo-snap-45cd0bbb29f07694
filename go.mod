// Path: go.mod | Codex for Claude Opus 5.5 | 2026-11-17
// WO-CORE-00 / WWPP-R41.1: pure-Go debug service dependencies.
module ccgsc/poolscore

go 1.26.0

require (
	github.com/chromedp/cdproto v0.160.1
	github.com/chromedp/chromedp v0.24.0
	github.com/jackc/pgx/v5 v5.13.0
	github.com/klauspost/compress v1.18.0
	golang.org/x/crypto v0.40.0
	golang.org/x/net v0.42.0
	// R15-FIX-CAPS (b): the Windows reader of the physical RAM uses the lazy
	// system DLL binding of golang.org/x/sys/windows. The module was already in
	// this go.mod (as an indirect dependency of the tree); the code of this work
	// order imports it directly, so it moves into the direct block. No new
	// module and no new version.
	golang.org/x/sys v0.49.0
	google.golang.org/grpc v1.76.0
	google.golang.org/protobuf v1.36.10
	modernc.org/sqlite v1.62.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250804133106-a7a43d27e69b // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
