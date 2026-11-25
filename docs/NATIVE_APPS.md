<!-- Path: docs/NATIVE_APPS.md | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-21
What/why: WO native hybrid apps - what each native app of the MVP does, how it is
built, run and checked, where the evidence lands, and which exit code means what.
One-command tools for the lab machines: no install, no PATH change, no network
beyond the Go module cache. -->

# Native hybrid apps

Three native binaries carry the MVP on a lab machine. They are built from the
same Go module (`ccgsc/poolscore`) that the rest of the tree uses, they need
no runtime, and one Python command builds all of them for every lab target.

| App | Kind | What it does |
|---|---|---|
| `pools-core` | service | The pool service itself: REST + gRPC + SSE, the two pools (`/thread-pool/v1`, `/process-pool/v1`), the engine, the store and the web pages under `/pools/`. |
| `pools-client` | CLI | The operator CLI. `check` runs the shared T108 check suite; `caps`, `submit`, `status`, `results`, `watch`, `worker` and `tui` drive a running server. |
| `pools-e2e` | verifier | The live end-to-end verifier. `-full` runs the same T108 suite; without `-full` it runs the single-pool REST path with the builtin worker or an external one (`-worker none`). |

`pools-migrate` stays in the main tree: it is a maintenance tool for the
database, not part of the lab machine set.

## Build

`scripts/build_native.py` cross-builds the three apps for the lab matrix
`windows/amd64`, `linux/amd64` and `linux/arm64` into `<out>/<os>_<arch>/`
and writes `<out>/SHA256SUMS.txt` with the relative names of the built files.

```
python pools-core/scripts/build_native.py --out dist
python pools-core/scripts/build_native.py --os windows --arch amd64 --out dist
python pools-core/scripts/build_native.py --os linux --arch arm64 --out dist
```

| Flag | Meaning |
|---|---|
| `--os` | `windows`, `linux` or `all`. Repeatable and comma separated. Default: every supported target. |
| `--arch` | `amd64`, `arm64` or `all`. Repeatable and comma separated. Default: every supported target. |
| `--out` | Distribution root. Default: `<work folder>/dist`, next to `pools-core`. |
| `--go` | Go binary to use. Default: `go` on `PATH`. |
| `--dry-run` | Print the build commands without running them. |

Every build uses `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"` and
`-buildvcs=false`, so the same source tree produces the same bytes on every
machine. `GOTOOLCHAIN` is pinned to `local` (override with
`POOLS_GO_TOOLCHAIN`), so the build never downloads a toolchain.

The distribution looks like this:

```
dist/
  SHA256SUMS.txt
  windows_amd64/  pools-client.exe  pools-core.exe  pools-e2e.exe
  linux_amd64/    pools-client      pools-core      pools-e2e
  linux_arm64/    pools-client      pools-core      pools-e2e
```

Verify a distribution with the standard tool:

```
cd dist && sha256sum -c SHA256SUMS.txt
```

The names in the manifest are relative and always use `/`, so the same file
checks out on Windows and on Linux.

## Run

### pools-core (the service)

```
pools-core -addr 127.0.0.1:8091 -database data/pools.db
pools-core -config config.example.json
```

Configuration comes from `config.example.json`, then the `POOLS_*` environment
variables, then the flags. The service needs a store (`POOLS_PG_DSN` selects
PostgreSQL, otherwise SQLite) and a lease store (`POOLS_LEASE_STORE=pg` or
`redis`; `redis` needs `POOLS_REDIS_ADDR`). `SIGINT` and `SIGTERM` shut the
server down gracefully. On the first start the seed admin password is written
to `admin-seed-credentials.txt` next to the database, never to stdout.

### pools-client (the operator CLI)

```
pools-client -base http://127.0.0.1:8091 -key-file /etc/pools/admin.key check
pools-client -base http://127.0.0.1:8091 -key-file /etc/pools/admin.key caps
pools-client -base http://127.0.0.1:8091 -key-file /etc/pools/admin.key submit -pool thread -size 160
pools-client -base http://127.0.0.1:8091 -key-file /etc/pools/admin.key status -pool thread <jobId>
```

The API key comes from `-key-file` or from `POOLS_CLIENT_KEY`. The `check`
subcommand takes `-pool` (`thread`, `process`, `both`), `-size` (160 or 240),
`-timeout` and `-out` (the JSON report path).

### pools-e2e (the verifier)

```
POOLS_E2E_ADMIN_KEY=... pools-e2e -base http://127.0.0.1:8091 -full -out pools-e2e.json
POOLS_E2E_ADMIN_KEY=... pools-e2e -base http://127.0.0.1:8091 -pool thread -worker builtin -out pools-e2e.json
```

`-full` runs the T108 suite, which is the same code `pools-client check` runs.
Without `-full` it runs the single-pool REST path; `-worker none` leaves the
worker to an external tab and writes the contributor key to a separate 0600
sidecar file instead of the report.

## Check

`scripts/pools_check.py` runs the suite and stores the evidence.

```
python pools-core/scripts/pools_check.py \
  --base http://127.0.0.1:8091 \
  --key-file /etc/pools/admin.key \
  --out evidence/2026-11-21-lab-node-02
```

| Flag | Meaning |
|---|---|
| `--base` | Server base URL. Default: `POOLS_BASE_URL` or `http://127.0.0.1:8091`. |
| `--key-file` | File that holds the admin API key. Required. |
| `--out` | Evidence folder. Created when missing. Required. |
| `--client` | The `pools-client` binary to run. Default: the one in `dist/` or `bin/`. |
| `--pool` | `both`, `thread` or `process`. Default: `both`. |
| `--size` | Matmul size of the submitted jobs. Default: `160`. |
| `--timeout` | Timeout of the suite itself, for example `3m`. Default: `3m`. |
| `--process-timeout` | Hard guard of the client process in seconds. Default: `900`. |

The script never reads the key and never prints it: it passes the *path* of
the key file to the client, and the client reads the key itself. The
captured output of the client is stored as evidence, not echoed.

The evidence folder holds four files:

| File | Content |
|---|---|
| `check-report.json` | The JSON report of the suite: one line per check, the `pass` flag, the revoked key count and the duration. |
| `summary.txt` | One line: `PASS` or `FAIL`, the base URL, the check count, the duration, the revoked key count, the client path and the report name. |
| `client-stdout.txt` | The table the client printed. |
| `client-stderr.txt` | What the client wrote to stderr; empty on a passing run. |

The suite is the shared `internal/checksuite` code, so `pools-client check`
and `pools-e2e -full` run exactly the same checks. Per pool it uploads one
unique module artifact, submits a job over REST and over gRPC, watches both
jobs over SSE (with a `Last-Event-ID` resume) and over the gRPC event
stream, starts the builtin worker only after the watches are established,
asserts the `queued -> leased -> done` order, checks the settled matmul
result against the reference hash, walks the anonymous contributor path and
revokes its own keys through the T99 route at the end. A full run is 25
checks over the two pools.

## Exit codes

### build_native.py

| Code | Meaning |
|---|---|
| 0 | Every requested binary was built and `SHA256SUMS.txt` was written. |
| 1 | A build failed. The Go output is printed to stderr. |
| 2 | Usage error: an unsupported target, a missing `go.mod`, or a bad flag value. |
| 3 | The Go toolchain was not found. |

### pools_check.py

| Code | Meaning |
|---|---|
| 0 | The suite passed (`pass: true` in the report). |
| 1 | The suite ran and at least one check failed. The report and the summary are still written. |
| 2 | Usage error: `--out` or `--key-file` is missing, or the key file does not exist. |
| 3 | No `pools-client` binary was found. |
| 4 | The client produced no usable JSON report (it could not reach the server, it crashed, or the report is not valid JSON). The stderr of the client is in the evidence folder. |

The two codes that matter for CI are 0 and 1: 0 means the lab machine is
healthy, 1 means the machine is up but the pool stack is not.

## Tests

```
python -m unittest discover -s pools-core/scripts/tests
```

`test_build_native.py` and `test_pools_check.py` are unittest, standard
library only, and never touch the network: the subprocess layer is mocked.
They pin the target matrix, the Go flags, the failure handling, the manifest
format, the summary line, the exit codes and the rule that the key value
never reaches an argument, an output file or a report.

## Notes and limits

* The build needs a Go 1.26 toolchain and Python 3. Nothing is installed and
  `PATH` is not changed.
* `CGO_ENABLED=0` means no cgo: the binaries are static and need no libc
  version from the lab image, but a cgo-only dependency could not be built.
* `-buildvcs=false` keeps the build reproducible in a copy of the tree that
  carries no repository.
* `pools-core` is a debug service: the default JWT secret is rejected in
  non-debug mode. Set `POOLS_JWT_SECRET` (32 bytes or more) for a lab run.
* The suite creates its own developer and contributor keys and revokes them
  again at the end. A run against a shared server leaves only the jobs and
  the artifacts of that run.
