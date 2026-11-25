# WO T24 - fix flaky launcher test (hermes MiniMax-M3 for Claude Opus 5.5)

Work folder: `F:\tmp\hcwork\t24\pools-core` · Date: 2026-11-17 · Author: hermes MiniMax-M3

## 1. Summary

The flaky test was **"CTRL_BREAK produces real Go graceful shutdown and server.stopped log"** in
`tests\launcher.Tests.ps1`. It was not a random flake: it was a **cold-start race between the
Roslyn C# compiler and the 5-second budget of the console-signal helper**.

`Stop-Core` (in `scripts\pools-core.ps1`) sends CTRL_BREAK to the service through a hidden helper
process. That helper compiled its P/Invoke shim **on the signal path** with
`Add-Type -TypeDefinition`. The Roslyn compile costs **~6.5 s cold** and **~0.5 s warm** on this
machine, while the helper was killed after **5000 ms**. On a cold machine the helper was therefore
killed *before it could deliver the signal*: Go never received SIGINT, the 12-second graceful
window expired, `Stop-Core` force-killed the process, and the test failed with
`Go did not exit successfully (expected '0', got '-1')`.

The fix removes the compiler from the signal path: the shim is compiled **once** into a
content-hashed assembly cache, and the helper only **loads** it (~50 ms). The Go code needed no
change — the race was entirely on the launcher side.

**No Go source file was modified.** `cmd\pools-core\main.go` and `internal\logx\logx.go` are
byte-for-byte unchanged.

## 2. Cause (measured, not guessed)

| # | Measurement | Value |
|---|---|---|
| 1 | Baseline test run, cold | **FAIL** — `Console signal helper timed out` → force kill → exit code `-1` |
| 2 | Baseline test run, warm | **PASS** |
| 3 | `Add-Type` compile inside a fresh pwsh, **cold** | **6441 ms** |
| 4 | `Add-Type` compile inside a fresh pwsh, warm | 426–863 ms |
| 5 | Whole helper child process, cold | 7865 ms |
| 6 | 5 warm helper spawns (wall) | 1509, 1826, 1509, 1816, 1512 ms — all under the 5000 ms budget |
| 7 | Helper loading a **precompiled** assembly (`Add-Type -Path`) | **45–53 ms** |

Conclusion: the 5 s helper budget was racing the **compiler warm-up**, not the signal. The test
passed whenever Roslyn happened to be warm, and failed whenever it was cold.

Ruled out along the way:

* **Not the Go side.** `runtime/os_windows.go` maps `CTRL_C_EVENT` **and** `CTRL_BREAK_EVENT` →
  `SIGINT` → `os/signal.Notify`, so CTRL_BREAK does reach Go's `signal.NotifyContext`. The
  graceful path works when the signal is actually delivered (exit code 0, `server.stopped` logged).
* **Not a log-flush race.** `internal/logx` writes each line with a single `Write` on an
  `O_APPEND` file handle, and `l.Info("server.stopped")` happens before `run()` returns, so the
  line is in the file before the process exits.
* **Not port reuse.** The fixture binds `127.0.0.1:0` (kernel-assigned port).
* **Not a timing issue in the Go shutdown.** The Go shutdown budget is 10 s and `Stop-Core` waits
  12 s; the failure was that the signal was never sent at all.

## 3. Fix

### 3.1 Product — `scripts\pools-core.ps1` (edited)

The compiler is moved **off** the signal path:

* `Get-CoreConsoleSource` — returns the P/Invoke shim source (verbatim from the original helper).
* `Get-CoreConsoleHelperPaths` — computes a **content hash (SHA-256, first 16 hex)** of that
  source and returns `<TEMP>\pools-core-console-helper\CoreConsole-<hash>.cs` / `.dll`. Because
  the name is content-hashed, a changed shim can never reuse a stale assembly and no cache
  invalidation logic is needed.
* `Ensure-CoreConsoleAssembly` — compiles the shim **once**, outside the signal path
  (`Add-Type -OutputAssembly` → atomic move into place).
* `Send-CoreBreak` — the helper now **loads** the cached assembly. If the cache is missing or
  unloadable, the helper falls back to compiling the shim itself, so the signal path never
  *depends* on the cache — it is only faster with it.
* Helper budget raised **5000 ms → 10000 ms**: still bounded, covers a cold fallback compile
  (~7 s measured) and still leaves `Stop-Core`'s 12 s graceful window intact.
* The helper's stdout/stderr are now captured and the **actual reason** is reported
  (`Could not send CTRL_BREAK to an isolated service console (CTRL_BREAK_FAILED: …)`) instead of
  a bare message, so the next failure is diagnosable.

### 3.2 Test — `tests\launcher.Tests.ps1` (edited)

* The fixture build now runs **from the project root** with the launcher's Go environment
  (`GOMODCACHE=F:\GoCache\mod`, `GOCACHE=F:\GoCache\build`, `GOTOOLCHAIN=local`). Previously it
  used the default module cache and **downloaded every dependency on a cold machine (95 s)** — a
  second, unrelated timing source inside the test.
* **Bounded poll** for the `server.start` marker (150 × 100 ms) instead of a fixed 50 iterations.
* **Bounded poll** for the `server.stopped` marker (50 × 100 ms) after exit, instead of a single
  read of the log file — the line is written by the Go process just before it exits.
* The fixture directory is now **removed** in the `finally` block (previously leaked into `%TEMP%`).

## 4. Proof

### 4.1 Ten consecutive runs of the launcher tests — 10/10 PASS

```
run  1: exit=0   12069 ms  passed: 17 | failed: 0  [PASS]
run  2: exit=0   14352 ms  passed: 17 | failed: 0  [PASS]
run  3: exit=0   12387 ms  passed: 17 | failed: 0  [PASS]
run  4: exit=0   11626 ms  passed: 17 | failed: 0  [PASS]
run  5: exit=0   11383 ms  passed: 17 | failed: 0  [PASS]
run  6: exit=0   11498 ms  passed: 17 | failed: 0  [PASS]
run  7: exit=0   11641 ms  passed: 17 | failed: 0  [PASS]
run  8: exit=0   11338 ms  passed: 17 | failed: 0  [PASS]
run  9: exit=0   11465 ms  passed: 17 | failed: 0  [PASS]
run 10: exit=0   14228 ms  passed: 17 | failed: 0  [PASS]

TALLY: pass=10 fail=0 of 10
```

Raw tally: `F:\tmp\hcwork\t24\RUN_TALLY.txt` · runner: `F:\tmp\hcwork\t24\run_tally.ps1`.

### 4.2 The original failure mode — cold cache, re-tested

The helper assembly cache was **deleted** (`%TEMP%\pools-core-console-helper`) and the suite re-run.
This is exactly the state that produced the original failure:

```
=== COLD CACHE RUN (original failure mode) ===
PASS  Stop-Core refuses force when process identity changes
Stopped the verified local process (PID 14544).
PASS  CTRL_BREAK produces real Go graceful shutdown and server.stopped log

=== summary ===
passed: 17
failed: 0
ALL TESTS PASSED
```

### 4.3 Go build / vet / test

```
=== go build ./... ===            exit=0
=== go vet ./... ===              exit=0
=== gofmt -l . ===                (empty — all files formatted)
=== go test -count=1 ./... ===
?   	ccgsc/poolscore/cmd/pools-core	[no test files]
?   	ccgsc/poolscore/internal/api	[no test files]
?   	ccgsc/poolscore/internal/auth	[no test files]
?   	ccgsc/poolscore/internal/config	[no test files]
?   	ccgsc/poolscore/internal/httpx	[no test files]
?   	ccgsc/poolscore/internal/logx	[no test files]
ok  	ccgsc/poolscore/internal/pool	2.459s
?   	ccgsc/poolscore/internal/store	[no test files]
ok  	ccgsc/poolscore/internal/store/pg	0.453s
?   	ccgsc/poolscore/internal/store/sqlite	[no test files]
ok  	ccgsc/poolscore/internal/verify	1.075s
?   	ccgsc/poolscore/internal/worker	[no test files]
ok  	ccgsc/poolscore/tests	3.174s
?   	ccgsc/poolscore/web	[no test files]
exit=0
```

### 4.4 Product path end-to-end

`launcher -Action start` → returns in **845 ms**; `launcher -Action stop` → returns in **1434 ms**,
service logs `server.stopped`, exit code **0**:

```
     Started pools-core (PID 11880).
     Mode      : DEBUG  (POOLS_LOG_LEVEL=debug, POOLS_DEBUG=1)
     Store     : sqlite  (driver=sqlite)
     Listen    : http://127.0.0.1:8091
...
     Stopped the verified local process (PID 11880).
```

### 4.5 Launcher test tail (final run)

```
PASS  PowerShell 7 is the host
PASS  pwsh -NoProfile -Command parser check on scripts\pools-core.ps1
PASS  launcher header keeps the required comment style
PASS  cmd launcher header keeps the required comment style
PASS  sh launcher header keeps the required comment style
PASS  pwsh -File scripts\pools-core.ps1 -Action status exits 0
PASS  pwsh -File scripts\pools-core.ps1 -Action diagnostics exits 0
PASS  pwsh -File scripts\pools-core.ps1 -Action config exits 0
PASS  pwsh -File scripts\pools-core.ps1 -Action logs exits 0 with no service running
PASS  every launcher action appends one JSON line to logs\launcher.jsonl
PASS  store selection writes POOLS_STORE into data\launcher.env.json
PASS  the .cmd launcher forwards an action to the ps1 launcher
PASS  browser address normalizes port-only and wildcard listeners
Stopped the verified local process (PID 123).
WARNING: Graceful shutdown timed out after 12 seconds; forcing the verified process to exit.
Stopped the verified local process (PID 123).
PASS  Stop-Core signals and waits before forcing only after timeout
PASS  Stop-Core refuses force when process identity changes
Stopped the verified local process (PID 13720).
PASS  CTRL_BREAK produces real Go graceful shutdown and server.stopped log

=== summary ===
passed: 17
failed: 0
ALL TESTS PASSED
```

(The two `WARNING`/`Stopped` lines belong to the `Stop-Core` **unit** test, which deliberately
simulates a non-graceful process; they are expected output, not failures.)

### 4.6 Compatibility checks

* `scripts\pools-core.ps1` parses clean under **PowerShell 7.6.9** (`PARSE_OK`).
* `scripts\pools-core.ps1` parses clean under **Windows PowerShell 5.1** (`PS51_PARSE_OK`).
* `tests\launcher.Tests.ps1` parses clean under PowerShell 7 (`PARSE_OK`).

## 5. Files changed

| File | Change |
|---|---|
| `scripts\pools-core.ps1` | Shim source extracted into `Get-CoreConsoleSource`; content-hashed assembly cache (`Get-CoreConsoleHelperPaths`, `Ensure-CoreConsoleAssembly`); `Send-CoreBreak` loads the cached assembly instead of compiling on the signal path; helper budget 5 s → 10 s; helper stdout/stderr captured and the real reason reported. |
| `tests\launcher.Tests.ps1` | Fixture build uses the launcher's Go env from the project root; bounded polls for `server.start` and `server.stopped`; fixture directory cleaned up in `finally`. |
| `cmd\pools-core\main.go` | **Unchanged** (the race was not in the product's Go code). |
| `internal\logx\logx.go` | **Unchanged** (log flush was not the cause). |

Work-order artifacts: `F:\tmp\hcwork\t24\PROGRESS.log`, `F:\tmp\hcwork\t24\RUN_TALLY.txt`,
`F:\tmp\hcwork\t24\run_tally.ps1`, `F:\tmp\hcwork\t24\probe\` (throwaway diagnostic probes).

## 6. Cleanup

* No server left running: `tasklist` shows **no** `pools-core.exe`; port **8091 is free**.
* No stray fixture directories in `%TEMP%` (3 pre-existing ones from the original runs removed;
  the test now removes its own).
* The helper assembly cache (`%TEMP%\pools-core-console-helper`) holds only the cached
  `CoreConsole-<hash>.cs` / `.dll`; the per-run `helper-*.txt` files are removed by the launcher.
* 3 stale `pools-core.exe` processes left over from an earlier diagnostic probe were terminated.

## 7. Notes / not done

* **The Go source was not modified** — the WO allowed it, but the evidence shows the race was in
  the launcher's signal path, so changing Go would have been the wrong fix.
* The fix is a **cache + load** rather than "a longer sleep": the signal path no longer performs
  a compiler invocation at all, so there is no warm-up to race. The 10 s helper budget is a
  bounded safety net for the cold fallback compile, not the mechanism of the fix.
* The helper assembly cache lives under `%TEMP%` and is rebuilt automatically if deleted (the
  launcher compiles it on demand); it is safe to delete at any time.
* An apparent ~192 s linger of `launcher -Action start` seen during diagnosis was an artifact of
  the **probe** capturing the child's inherited pipe handle, not a launcher defect — with output
  redirected to a file the launcher returns in 845 ms (§4.4).

DONE T24
