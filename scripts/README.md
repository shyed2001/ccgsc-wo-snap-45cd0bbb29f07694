<!-- Path: scripts/README.md | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
What/why: usage and contract notes for the pools-core launcher set (WO-T5, WWPP-R41.1).
WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the monitor/admin links now open the real
/pools/monitor.html page served by pools-core, not the old /web/ placeholders. -->

# pools-core launcher

Three launchers drive the same local Go service (`cmd/pools-core`) from one work folder.
None of them installs anything, changes `PATH`, or touches the network beyond the Go module cache.

| File | Platform | Use |
|---|---|---|
| `pools-core.cmd` | Windows | double-click entry point; also `pools-core.cmd start\|stop\|status` |
| `pools-core.ps1` | Windows (PowerShell 7+) | full TUI menu plus every non-interactive action |
| `pools-core.sh` | Linux / macOS (bash) | the same actions, no interactive menu |

All three read and write the same state inside the project folder:

| Path | Meaning |
|---|---|
| `bin/pools-core.exe` / `bin/pools-core` | the built service binary (rebuilt when sources are newer) |
| `data/pools-core.pid` | PID of the running service; the executable path is verified before any stop |
| `data/launcher.env.json` | launcher environment (`POOLS_*`); `start` reads it |
| `logs/pools-core-YYYYMMDD.jsonl` | the service's own JSON log (UTC day rotation) |
| `logs/launcher-*.stdout.log` / `.stderr.log` | stdout/stderr of the launched process |
| `logs/launcher.jsonl` | one JSON line per launcher action (`ts`, `action`, `result`, `ms`) |

## Windows: double click

Double-click `pools-core.cmd`. It opens the PowerShell 7 TUI menu on one screen:

```
 1) Start (debug)      POOLS_LOG_LEVEL=debug + POOLS_DEBUG=1
 2) Start (normal)     POOLS_LOG_LEVEL=info  + POOLS_DEBUG=0
 3) Stop               stop the verified local process
 4) Restart            stop, then start in the current mode
 5) Status             PID, port, uptime and the /healthz JSON body
 6) Live log tail      follow logs\pools-core-*.jsonl (Ctrl+C returns to the menu)
 7) Open Monitor page  http://127.0.0.1:8091/pools/monitor.html
 8) Open Admin page    http://127.0.0.1:8091/pools/monitor.html
 9) Store backend      sqlite | json | csv | postgres | redis -> POOLS_STORE
10) Show config        effective configuration, launcher env file, store catalog
11) Diagnostics        toolchain, binary, pid, port, health, log locations
12) Test (build+test)  go build ./... + go vet ./... + go test ./...
 Q) Quit
```

Every entry has a one-line legend under it. The launcher always prints which mode is active
(`Mode : DEBUG (POOLS_LOG_LEVEL=debug, POOLS_DEBUG=1)`) when it starts the service.

## Windows: non-interactive

```
scripts\pools-core.cmd start        rem or: stop | restart | status | logs | tail
scripts\pools-core.cmd config
scripts\pools-core.cmd store sqlite
scripts\pools-core.cmd diagnostics
scripts\pools-core.cmd test

pwsh -NoProfile -File scripts\pools-core.ps1 -Action status
pwsh -NoProfile -File scripts\pools-core.ps1 -Action diagnostics
pwsh -NoProfile -File scripts\pools-core.ps1 -Action tail -Level error
```

Arguments passed to `pools-core.cmd` are forwarded verbatim to `pools-core.ps1`.
Every action exits `0` on success and `1` on failure, so it can be used from CI or another script.

## Linux / macOS

```
scripts/pools-core.sh start          # debug mode
scripts/pools-core.sh start-normal
scripts/pools-core.sh stop
scripts/pools-core.sh status
scripts/pools-core.sh logs 100
scripts/pools-core.sh tail error
scripts/pools-core.sh store sqlite
scripts/pools-core.sh config
scripts/pools-core.sh diagnostics
scripts/pools-core.sh test
scripts/pools-core.sh help
```

The shell launcher writes the same `logs/launcher.jsonl` lines and the same `data/launcher.env.json`.

## Store backend selection

Menu item `9` (or `-Action store -Store <name>`, or `pools-core.sh store <name>`) writes
`POOLS_STORE` into `data/launcher.env.json`. The next `start` reads it.

| Name | Status in this build | Meaning |
|---|---|---|
| `sqlite` | implemented | modernc SQLite file, default `data/pools.db` |
| `postgres` | stub | the pgx seam exists but `store.Open` returns `not-implemented` |
| `json` | missing | no JSON store adapter in this build |
| `csv` | missing | no CSV store adapter in this build |
| `redis` | missing | no Redis store adapter in this build |

Selecting an unimplemented backend is allowed and is recorded, but `start` refuses it with a
clear message instead of silently falling back to SQLite. `postgres` is allowed to start and
fails inside the service with the explicit `not-implemented` error, exactly as the Go stub does.

## Debug mode

Debug mode sets `POOLS_LOG_LEVEL=debug` and `POOLS_DEBUG=1` in the environment of the started
process. The Go service logs JSON lines to stdout and to `logs/pools-core-YYYYMMDD.jsonl`.

Normal mode sets `POOLS_LOG_LEVEL=info` and `POOLS_DEBUG=0`. The Go config rejects the default
debug JWT secret in non-debug mode, so the launcher generates a local-only 32-byte secret and
stores it in `data/launcher.env.json` (the value is never printed and is redacted in
`Show config` output).

## Tests

```
pwsh -NoProfile -File tests\launcher.Tests.ps1
```

Pester-free: a plain script that runs the launcher actions, parses `scripts/pools-core.ps1`
with the PowerShell AST parser, and checks exit codes and the `logs/launcher.jsonl` contract.

## Notes and limits

* The launcher only ever touches this project folder and `F:\GoCache` (Go module/build cache).
* `Stop` on Windows uses `Stop-Process` after verifying the PID belongs to `bin/pools-core.exe`;
  it is forceful, not a claimed graceful SIGINT shutdown. The shell launcher sends `SIGTERM`,
  which the service handles as a graceful shutdown.
* `POOLS_ADDR` (environment) wins over `data/launcher.env.json`, which wins over
  `config.example.json`, which wins over the default `127.0.0.1:8091`.
* `Open Monitor page` / `Open Admin page` are intentional visible browser actions.
