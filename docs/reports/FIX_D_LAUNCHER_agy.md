# WO FIX-D - Launcher Hardening Report

- **Date:** 2026-11-17
- **Target Folder:** `F:\tmp\par\fixd\pools-core`
- **Source Work Order:** `docs/work_orders/WO_FIX_D_LAUNCHER.md`
- **Findings Reference:** `docs/reviews/WEBCHAT_R1_minimax_launcher/FINDINGS.md`
- **Modified Files:**
  - `scripts/pools-core.ps1`
  - `scripts/pools-core.sh`
  - `tests/launcher.Tests.ps1`
- **Untouched Files:** All Go code (`cmd/`, `internal/`, `web/`) left untouched as instructed.

---

## Row-by-Row Resolution (3 MAJOR + 9 MINOR)

### 1. MAJOR: Secret set in debug mode too
- **Finding (`3_pools-core.ps1:254`):** In debug mode, `Ensure-NormalSecret` was never called (debug was the default mode), leaving `POOLS_JWT_SECRET` unset and forcing the service to fall back to unconfigured/empty defaults.
- **Fix:**
  - `scripts/pools-core.ps1`: Renamed/aliased secret retrieval to `Ensure-Secret`, invoked unconditionally in `Start-Core` before mode branching, setting `$env:POOLS_JWT_SECRET = $secret` in both debug and normal modes.
  - `scripts/pools-core.sh`: Updated `do_start` to ensure `POOLS_JWT_SECRET` is generated or read and exported in both debug and normal modes.
- **Test:** Added `WO-FIX-D Row 1: secret set in debug mode too` verifying `Ensure-Secret` returns a 64-char hex key and `Start-Core` unconditionally assigns `$env:POOLS_JWT_SECRET`.

### 2. MAJOR: Secret never stored in cleartext (DPAPI + restricted ACL; chmod 600)
- **Finding (`3_pools-core.ps1:232`):** JWT secret was written in plain cleartext into `data/launcher.env.json` with inherited ACLs, allowing any local reader of the project directory to read the signing key.
- **Fix:**
  - `scripts/pools-core.ps1`: Implemented `Protect-SecretString` and `Unprotect-SecretString` utilizing DPAPI (`ConvertTo-SecureString` and `ConvertFrom-SecureString`) tied to the current Windows user. Implemented `Protect-FileAcl` to disable inheritance and grant `FullControl` solely to the current user on `data/launcher.env.json`.
  - `scripts/pools-core.sh`: Restricts file permissions on `data/launcher.env.json` to user-only (`chmod 600`) and `data/` directory (`chmod 700`) with an explicit note documenting POSIX file permission enforcement complementary to Windows DPAPI/ACL.
- **Test:** Added `WO-FIX-D Row 2: secret never stored in cleartext (DPAPI + restricted ACL)` verifying `data/launcher.env.json` raw file content does not contain cleartext hex secret, round-trip decryption works, and Windows ACL inheritance is disabled with user-only permissions.

### 3. MAJOR: PID file stores pid + exe path + StartTime and every signal verifies all three first
- **Finding (`3_pools-core.ps1:274, 351`):** PID file recorded only PID integer (`4242`). `Stop-Core` signaled the process with `Send-CoreBreak` prior to verifying identity, allowing recycled PIDs to send console break signals to unrelated processes.
- **Fix:**
  - `scripts/pools-core.ps1`: Recorded JSON structure `{"pid": <id>, "path": "<binary>", "startTime": "<iso_timestamp>"}` into `$pidFile`. In `Stop-Core`, added identity validation checking PID, executable path, and StartTime before invoking `Send-CoreBreak`.
  - `scripts/pools-core.sh`: Records JSON with pid, binary path, and `ps -o lstart=` timestamp. In `do_stop`, verifies pid, exe link, and start time before sending `kill -TERM`.
- **Test:** Added `WO-FIX-D Row 3: PID file records pid + path + StartTime and Stop-Core verifies all three` verifying JSON PID parsing and that `Stop-Core` throws and refuses to signal when path or start time mismatches.

### 4. MINOR: Allow-list redaction in Show-Config
- **Finding (`3_pools-core.ps1:442`):** `Show-Config` redacted keys matching a regex (`SECRET|KEY|TOKEN|PASSWORD`), leaking non-matching sensitive keys such as `POOLS_SIGNING` and `POOLS_CREDENTIAL`.
- **Fix:**
  - `scripts/pools-core.ps1`: Inverted redaction in `Show-Config` to an explicit allowlist of known-safe keys (`POOLS_ADDR`, `POOLS_DEBUG`, `POOLS_LOG_LEVEL`, `POOLS_STORE`, `updatedAt`). Any key not in the allowlist is masked as `<redacted>`.
  - `scripts/pools-core.sh`: Updated `do_config` with `awk` allowlist filter redacting any non-safe keys.
- **Test:** Added `WO-FIX-D Row 4: allow-list redaction in Show-Config` verifying `POOLS_ADDR` is displayed in clear while `POOLS_SIGNING` and `POOLS_CREDENTIAL` are masked as `<redacted>`.

### 5. MINOR: Redact secret-shaped substrings in Show-Logs/Live tail
- **Finding (`3_pools-core.ps1:390, 399`):** Service log lines were printed directly without filtering, leaking logs like `{"level":"debug","msg":"jwt secret is abc123"}` or tokens.
- **Fix:**
  - `scripts/pools-core.ps1`: Added `Redact-LogLine` filtering Bearer tokens, JSON sensitive keys, `secret is <val>`, and 32-64 char hex strings; wired into `Show-Logs` and `Invoke-LiveTail`.
  - `scripts/pools-core.sh`: Added `redact_log_stream` filtering `Bearer`, sensitive key patterns, and hex strings across `do_logs` and `do_tail`.
- **Test:** Added `WO-FIX-D Row 5: redact secret-shaped substrings in Show-Logs/Live tail` testing lines containing hex hashes, sensitive keys, and Bearer headers.

### 6. MINOR: Newest log by LastWriteTimeUtc
- **Finding (`3_pools-core.ps1:185`):** `Get-LatestServiceLog` sorted by file name, mistakenly picking `pools-core-debug.jsonl` over newer timestamped log files.
- **Fix:**
  - `scripts/pools-core.ps1`: Changed sorting in `Get-LatestServiceLog` to `Sort-Object LastWriteTimeUtc`.
  - `scripts/pools-core.sh`: Changed `latest_service_log` to `ls -1t ... | head -n1`.
- **Test:** Added `WO-FIX-D Row 6: newest log selected by LastWriteTimeUtc` verifying a file with older name but newer timestamp is chosen.

### 7. MINOR: Go cache dirs only if the path exists
- **Finding (`3_pools-core.ps1:23`):** `GOMODCACHE` and `GOCACHE` were unconditionally assigned to hardcoded `F:\GoCache\...` paths, breaking execution on non-F: machines.
- **Fix:**
  - `scripts/pools-core.ps1`: Added `Test-Path` checks before setting `$env:GOMODCACHE` and `$env:GOCACHE`.
  - `scripts/pools-core.sh`: Confirmed existing `${GOMODCACHE:-}` guards.
- **Test:** Added `WO-FIX-D Row 7: Go cache dirs only set if paths exist` asserting the `Test-Path` guard exists in the script AST.

### 8. MINOR: Warn/refuse non-loopback POOLS_ADDR unless -AllowRemote
- **Finding (`3_pools-core.ps1:96`):** `Get-EffectiveAddress` permitted `0.0.0.0` with no warning while browser URL remapped to loopback, masking remote exposure.
- **Fix:**
  - `scripts/pools-core.ps1`: Added `[switch]$AllowRemote` parameter. In `Start-Core`, validated that non-loopback address is refused with an exception unless `-AllowRemote` is passed.
  - `scripts/pools-core.sh`: Checked address host and refuses non-loopback unless `ALLOW_REMOTE=1` / `--allow-remote` is passed.
- **Test:** Added `WO-FIX-D Row 8: refuse non-loopback POOLS_ADDR unless -AllowRemote` verifying `0.0.0.0` is recognized as non-loopback and refused unless `-AllowRemote` is set.

### 9. MINOR: Readiness bound to the started PID
- **Finding (`3_pools-core.ps1:279`):** Readiness check polled `$address` without verifying whether the listening process was the newly started process.
- **Fix:**
  - `scripts/pools-core.ps1`: In the readiness loop, re-checked `$process.HasExited` and validated that if `/healthz` returns a PID, it equals `$process.Id`.
  - `scripts/pools-core.sh`: Re-verified process liveness and PID in `/healthz` response.
- **Test:** Added `WO-FIX-D Row 9: readiness bound to the started PID` verifying AST and runtime checks for PID alignment and exit re-checks.

### 10. MINOR: Cleanup on failed start
- **Finding (`3_pools-core.ps1:283`):** If the health check timed out or failed, the function threw after writing the PID file and spawning the process, leaving an orphaned process and stale PID file.
- **Fix:**
  - `scripts/pools-core.ps1`: Added `try ... catch` block around startup loop that kills the process and zeroes `$pidFile` to `'0'` upon failure before re-throwing.
  - `scripts/pools-core.sh`: Added termination and `'0' > "$PID_FILE"` on timeout/exit failure in `do_start`.
- **Test:** Added `WO-FIX-D Row 10: cleanup on failed start stops process and zeroes PID file` verifying cleanup logic.

### 11. MINOR: No name-only process match
- **Finding (`3_pools-core.ps1:145`):** `Get-CoreProcess` fell back to matching by process name `'pools-core'` when `Path` was inaccessible, risking signaling unrelated processes on recycled PIDs.
- **Fix:**
  - `scripts/pools-core.ps1`: Removed name-only fallback; requires process path verification against expected binary / PID record path.
  - `scripts/pools-core.sh`: Enforced executable path verification; refuses match on command line / process name alone.
- **Test:** Added `WO-FIX-D Row 11: no name-only process match in Get-CoreProcess` verifying that an unverified or mismatched path returns `$null`.

### 12. MINOR: StartTime read inside try/catch
- **Finding (`3_pools-core.ps1:355`):** `Stop-Core` accessed `$verified.StartTime` outside a `try/catch`, which if throwing prevented shutdown and force stop.
- **Fix:**
  - `scripts/pools-core.ps1`: Wrapped both `$verified.StartTime` and `$proc.StartTime` reads inside `try/catch`. If null or throwing, treated as identity unknown / changed and force stop is refused.
  - `scripts/pools-core.sh`: Wrapped timestamp queries so any error safely avoids unintended termination.
- **Test:** Added `WO-FIX-D Row 12: StartTime read inside try/catch during process verification` with a mock throwing Win32Exception on `StartTime` access, confirming it is caught safely.

---

## Verification Runs

### Three Consecutive Test Runs (`pwsh -NoProfile -File tests\launcher.Tests.ps1`)

#### Run 1 Output Tail
```
PASS  WO-FIX-D Row 1: secret set in debug mode too
PASS  WO-FIX-D Row 2: secret never stored in cleartext (DPAPI + restricted ACL)
PASS  WO-FIX-D Row 3: PID file records pid + path + StartTime and Stop-Core verifies all three
PASS  WO-FIX-D Row 4: allow-list redaction in Show-Config
PASS  WO-FIX-D Row 5: redact secret-shaped substrings in Show-Logs/Live tail
PASS  WO-FIX-D Row 6: newest log selected by LastWriteTimeUtc
PASS  WO-FIX-D Row 7: Go cache dirs only set if paths exist
PASS  WO-FIX-D Row 8: refuse non-loopback POOLS_ADDR unless -AllowRemote
PASS  WO-FIX-D Row 9: readiness bound to the started PID
PASS  WO-FIX-D Row 10: cleanup on failed start stops process and zeroes PID file
PASS  WO-FIX-D Row 11: no name-only process match in Get-CoreProcess
PASS  WO-FIX-D Row 12: StartTime read inside try/catch during process verification

=== summary ===
passed: 29
failed: 0
ALL TESTS PASSED
```

#### Run 2 Output Tail
```
PASS  WO-FIX-D Row 1: secret set in debug mode too
PASS  WO-FIX-D Row 2: secret never stored in cleartext (DPAPI + restricted ACL)
PASS  WO-FIX-D Row 3: PID file records pid + path + StartTime and Stop-Core verifies all three
PASS  WO-FIX-D Row 4: allow-list redaction in Show-Config
PASS  WO-FIX-D Row 5: redact secret-shaped substrings in Show-Logs/Live tail
PASS  WO-FIX-D Row 6: newest log selected by LastWriteTimeUtc
PASS  WO-FIX-D Row 7: Go cache dirs only set if paths exist
PASS  WO-FIX-D Row 8: refuse non-loopback POOLS_ADDR unless -AllowRemote
PASS  WO-FIX-D Row 9: readiness bound to the started PID
PASS  WO-FIX-D Row 10: cleanup on failed start stops process and zeroes PID file
PASS  WO-FIX-D Row 11: no name-only process match in Get-CoreProcess
PASS  WO-FIX-D Row 12: StartTime read inside try/catch during process verification

=== summary ===
passed: 29
failed: 0
ALL TESTS PASSED
```

#### Run 3 Output Tail
```
PASS  WO-FIX-D Row 1: secret set in debug mode too
PASS  WO-FIX-D Row 2: secret never stored in cleartext (DPAPI + restricted ACL)
PASS  WO-FIX-D Row 3: PID file records pid + path + StartTime and Stop-Core verifies all three
PASS  WO-FIX-D Row 4: allow-list redaction in Show-Config
PASS  WO-FIX-D Row 5: redact secret-shaped substrings in Show-Logs/Live tail
PASS  WO-FIX-D Row 6: newest log selected by LastWriteTimeUtc
PASS  WO-FIX-D Row 7: Go cache dirs only set if paths exist
PASS  WO-FIX-D Row 8: refuse non-loopback POOLS_ADDR unless -AllowRemote
PASS  WO-FIX-D Row 9: readiness bound to the started PID
PASS  WO-FIX-D Row 10: cleanup on failed start stops process and zeroes PID file
PASS  WO-FIX-D Row 11: no name-only process match in Get-CoreProcess
PASS  WO-FIX-D Row 12: StartTime read inside try/catch during process verification

=== summary ===
passed: 29
failed: 0
ALL TESTS PASSED
```

### Bash Syntax Check (`bash -n scripts/pools-core.sh`)
```
Command: bash -n scripts/pools-core.sh
Exit Code: 0
Output: (clean, no syntax errors)
```

DONE FIXD
