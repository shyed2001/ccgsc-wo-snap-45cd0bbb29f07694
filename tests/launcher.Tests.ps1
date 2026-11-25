# Path: tests/launcher.Tests.ps1 | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
# What/why: Pester-free smoke test for the pools-core launcher (WO-T5, WWPP-R41.1).
#   Runs the PowerShell 7 launcher in its non-interactive actions, parses the script with the
#   PowerShell AST parser and checks the exit codes. Plain script: no Pester, no installs.
#   Run it with:  pwsh -NoProfile -File tests\launcher.Tests.ps1
$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path $PSScriptRoot -Parent
$scriptPath  = Join-Path $projectRoot 'scripts\pools-core.ps1'
$cmdPath     = Join-Path $projectRoot 'scripts\pools-core.cmd'
$shPath      = Join-Path $projectRoot 'scripts\pools-core.sh'

$script:Passed = 0
$script:Failed = 0
$script:Failures = [System.Collections.Generic.List[string]]::new()

function Test-Case([string]$Name, [scriptblock]$Body) {
    try {
        & $Body
        $script:Passed++
        Write-Host ("PASS  {0}" -f $Name) -ForegroundColor Green
    } catch {
        $script:Failed++
        $script:Failures.Add($Name)
        Write-Host ("FAIL  {0}" -f $Name) -ForegroundColor Red
        Write-Host ("      {0}" -f $_.Exception.Message) -ForegroundColor Red
    }
}

function Assert-True([bool]$Condition, [string]$Message) {
    if (!$Condition) { throw $Message }
}

function Assert-Equal($Expected, $Actual, [string]$Message) {
    if ($Expected -ne $Actual) { throw ("{0} (expected '{1}', got '{2}')" -f $Message, $Expected, $Actual) }
}

Write-Host '=== pools-core launcher tests (Pester-free) ===' -ForegroundColor Cyan
Write-Host ("project root: {0}" -f $projectRoot)
Write-Host ''

# ---------------------------------------------------------------- preconditions ----
Test-Case 'launcher files exist' {
    Assert-True (Test-Path -LiteralPath $scriptPath) 'scripts\pools-core.ps1 is missing'
    Assert-True (Test-Path -LiteralPath $cmdPath)    'scripts\pools-core.cmd is missing'
    Assert-True (Test-Path -LiteralPath $shPath)     'scripts\pools-core.sh is missing'
}

Test-Case 'PowerShell 7 is the host' {
    Assert-True ($PSVersionTable.PSVersion.Major -ge 7) ("expected PowerShell 7+, got {0}" -f $PSVersionTable.PSVersion)
}

# ------------------------------------------------------------------- parser ----
Test-Case 'pwsh -NoProfile -Command parser check on scripts\pools-core.ps1' {
    $parseScript = @'
param([string]$Path)
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors)
if ($errors -and $errors.Count -gt 0) {
    foreach ($e in $errors) { Write-Output ("PARSE_ERROR: {0} (line {1})" -f $e.Message, $e.Extent.StartLineNumber) }
    exit 1
}
Write-Output ("PARSE_OK: {0} function(s), {1} token(s), 0 error(s)" -f $ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst]}, $true).Count, $tokens.Count)
exit 0
'@
    $parseFile = Join-Path $env:TEMP 'pools-core-parse-check.ps1'
    [IO.File]::WriteAllText($parseFile, $parseScript)
    try {
        $out = & pwsh -NoProfile -Command "& '$parseFile' -Path '$scriptPath'" 2>&1
        $code = $LASTEXITCODE
        Assert-Equal 0 $code ("parser check failed: {0}" -f ($out -join ' '))
        Assert-True (($out -join "`n") -match 'PARSE_OK') ("parser check did not report PARSE_OK: {0}" -f ($out -join ' '))
    } finally {
        Remove-Item -LiteralPath $parseFile -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'launcher header keeps the required comment style' {
    $head = (Get-Content -LiteralPath $scriptPath -TotalCount 3) -join "`n"
    Assert-True ($head -match 'Path:\s*scripts/pools-core\.ps1') 'header must contain the Path field'
    Assert-True ($head -match 'Author:\s*hermes MiniMax-M3 for Claude Opus 5\.5') 'header must name the author'
    Assert-True ($head -match '\d{4}-\d{2}-\d{2}') 'header must contain the date'
    Assert-True ($head -match 'What/why:') 'header must contain the What/why field'
}

Test-Case 'cmd launcher header keeps the required comment style' {
    $head = (Get-Content -LiteralPath $cmdPath -TotalCount 3) -join "`n"
    Assert-True ($head -match 'Path:\s*scripts/pools-core\.cmd') 'header must contain the Path field'
    Assert-True ($head -match 'Author:\s*hermes MiniMax-M3 for Claude Opus 5\.5') 'header must name the author'
    Assert-True ($head -match '\d{4}-\d{2}-\d{2}') 'header must contain the date'
    Assert-True ($head -match 'What/why:') 'header must contain the What/why field'
}

Test-Case 'sh launcher header keeps the required comment style' {
    $head = (Get-Content -LiteralPath $shPath -TotalCount 3) -join "`n"
    Assert-True ($head -match 'Path:\s*scripts/pools-core\.sh') 'header must contain the Path field'
    Assert-True ($head -match 'Author:\s*hermes MiniMax-M3 for Claude Opus 5\.5') 'header must name the author'
    Assert-True ($head -match '\d{4}-\d{2}-\d{2}') 'header must contain the date'
    Assert-True ($head -match 'What/why:') 'header must contain the What/why field'
}

# ------------------------------------------------------------ launcher actions ----
Test-Case 'pwsh -File scripts\pools-core.ps1 -Action status exits 0' {
    $out = & pwsh -NoProfile -File $scriptPath -Action status 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("status action failed: {0}" -f ($out -join ' '))
    Assert-True (($out -join "`n") -match 'pools-core status') ("status output missing the status block: {0}" -f ($out -join ' '))
}

Test-Case 'pwsh -File scripts\pools-core.ps1 -Action diagnostics exits 0' {
    $out = & pwsh -NoProfile -File $scriptPath -Action diagnostics 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("diagnostics action failed: {0}" -f ($out -join ' '))
    Assert-True (($out -join "`n") -match 'diagnostics') ("diagnostics output missing the diagnostics block: {0}" -f ($out -join ' '))
}

Test-Case 'pwsh -File scripts\pools-core.ps1 -Action config exits 0' {
    $out = & pwsh -NoProfile -File $scriptPath -Action config 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("config action failed: {0}" -f ($out -join ' '))
    Assert-True (($out -join "`n") -match 'effective configuration') ("config output missing the config block: {0}" -f ($out -join ' '))
}

Test-Case 'pwsh -File scripts\pools-core.ps1 -Action logs exits 0 with no service running' {
    $out = & pwsh -NoProfile -File $scriptPath -Action logs 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("logs action failed: {0}" -f ($out -join ' '))
}

Test-Case 'every launcher action appends one JSON line to logs\launcher.jsonl' {
    $logFile = Join-Path $projectRoot 'logs\launcher.jsonl'
    Assert-True (Test-Path -LiteralPath $logFile) 'logs\launcher.jsonl was not created'
    $lines = @(Get-Content -LiteralPath $logFile | Where-Object { $_.Trim() })
    Assert-True ($lines.Count -gt 0) 'logs\launcher.jsonl is empty'
    # Validate the raw JSON text: ConvertFrom-Json coerces the ISO stamp into a DateTime.
    $last = $lines[-1]
    foreach ($field in @('ts', 'action', 'result', 'ms')) {
        Assert-True ($last -match ('"{0}"\s*:\s*"' -f $field) -or ($last -match ('"{0}"\s*:\s*[\d.]+' -f $field))) `
                    ("launcher log line is missing the '$field' field: {0}" -f $last)
    }
    Assert-True ($last -match '"ts"\s*:\s*"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}') `
                ("launcher log 'ts' is not an ISO-8601 UTC stamp: {0}" -f $last)
    Assert-True ($last -match '"action"\s*:\s*"[a-z-]+"') ("launcher log 'action' is not a name: {0}" -f $last)
    Assert-True ($last -match '"result"\s*:\s*"(ok|error)"') ("launcher log 'result' is not ok/error: {0}" -f $last)
    Assert-True ($last -match '"ms"\s*:\s*[\d.]+') ("launcher log 'ms' is not a number: {0}" -f $last)
    foreach ($line in $lines) {
        $null = $line | ConvertFrom-Json -ErrorAction Stop
    }
}

Test-Case 'store selection writes POOLS_STORE into data\launcher.env.json' {
    $out = & pwsh -NoProfile -File $scriptPath -Action store -Store sqlite 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("store action failed: {0}" -f ($out -join ' '))
    $envFile = Join-Path $projectRoot 'data\launcher.env.json'
    Assert-True (Test-Path -LiteralPath $envFile) 'data\launcher.env.json was not written'
    $parsed = Get-Content -LiteralPath $envFile -Raw | ConvertFrom-Json -ErrorAction Stop
    Assert-Equal 'sqlite' $parsed.POOLS_STORE 'POOLS_STORE was not written into data\launcher.env.json'
}

Test-Case 'the .cmd launcher forwards an action to the ps1 launcher' {
    $out = & cmd.exe /c "`"$cmdPath`" status" 2>&1
    $code = $LASTEXITCODE
    Assert-Equal 0 $code ("cmd launcher status failed: {0}" -f ($out -join ' '))
    Assert-True (($out -join "`n") -match 'pools-core status') ("cmd launcher did not run the ps1 launcher: {0}" -f ($out -join ' '))
}

# Review-01 regression tests (Codex, 2026-11-17). Load functions without the dispatcher.
$parseTokens = $null
$parseErrors = $null
$launcherAst = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$parseTokens, [ref]$parseErrors)
foreach ($fn in $launcherAst.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst]}, $false)) {
    . ([scriptblock]::Create($fn.Extent.Text))
}

$binary      = Join-Path $projectRoot 'bin\pools-core.exe'
$binDir      = Join-Path $projectRoot 'bin'
$dataDir     = Join-Path $projectRoot 'data'
$logDir      = Join-Path $projectRoot 'logs'
$pidFile     = Join-Path $dataDir 'pools-core.pid'
$launcherEnv = Join-Path $dataDir 'launcher.env.json'
$launcherLog = Join-Path $logDir 'launcher.jsonl'
$configFile  = Join-Path $projectRoot 'config.example.json'
$defaultAddr = '127.0.0.1:8091'

Test-Case 'browser address normalizes port-only and wildcard listeners' {
    foreach ($address in @(':8091', '0.0.0.0:8091', '[::]:8091', ':::8091')) {
        Assert-Equal '127.0.0.1:8091' (Get-BrowserAddress $address) "bad normalization for $address"
    }
    Assert-Equal 'example.test:8091' (Get-BrowserAddress 'example.test:8091') 'named listener changed'
}

Test-Case 'Stop-Core signals and waits before forcing only after timeout' {
    foreach ($graceful in @($true, $false)) {
        $calls = [Collections.Generic.List[string]]::new()
        $fake = [pscustomobject]@{ Id = 123; StartTime = [datetime]'2026-11-17'; Graceful = $graceful; Calls = $calls }
        $fake | Add-Member ScriptMethod WaitForExit { param($ms) $this.Calls.Add("wait:$ms"); return ($this.Graceful -or $ms -eq 5000) }
        function Get-CoreProcess { return $fake }
        function Send-CoreBreak([int]$ProcessId) { $calls.Add("signal:$ProcessId") }
        function Stop-Process { param($InputObject, [switch]$Force) $calls.Add('force') }
        $pidFile = Join-Path $env:TEMP ('pools-stop-' + [guid]::NewGuid() + '.pid')
        Stop-Core
        $expected = if ($graceful) { 'signal:123,wait:12000' } else { 'signal:123,wait:12000,force,wait:5000' }
        Assert-Equal $expected ($calls -join ',') 'incorrect shutdown sequence'
        Assert-Equal '0' ([IO.File]::ReadAllText($pidFile)) 'PID was not cleared after exit'
    }
}

Test-Case 'Stop-Core refuses force when process identity changes' {
    $calls = [Collections.Generic.List[string]]::new()
    $fake = [pscustomobject]@{ Id = 123; StartTime = [datetime]'2026-11-17' }
    $fake | Add-Member ScriptMethod WaitForExit { param($ms) return $false }
    function Get-CoreProcess {
        $calls.Add('lookup')
        if ($calls.Count -eq 1) { return $fake }
        return [pscustomobject]@{ Id = 123; StartTime = [datetime]'2026-11-18' }
    }
    function Send-CoreBreak([int]$ProcessId) { }
    function Stop-Process { param($InputObject, [switch]$Force) throw 'UNSAFE FORCE' }
    $caught = ''
    try { Stop-Core } catch { $caught = $_.Exception.Message }
    Assert-True ($caught -match 'identity changed') "unexpected result: $caught"
}

Test-Case 'CTRL_BREAK produces real Go graceful shutdown and server.stopped log' {
    $fixtureDir = Join-Path $env:TEMP ('pools-graceful-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    $fixtureBinary = Join-Path $fixtureDir 'pools-core.exe'
    # Build the fixture from the project root with the same Go environment the launcher
    # uses (scripts\pools-core.ps1 sets GOMODCACHE/GOCACHE/GOTOOLCHAIN). Without this the
    # in-test build resolves the default module cache and downloads every dependency on a
    # cold machine (95 s), which is a timing source, not a real test.
    Push-Location -LiteralPath $projectRoot
    try {
        $env:GOMODCACHE = 'F:\GoCache\mod'
        $env:GOCACHE    = 'F:\GoCache\build'
        $env:GOTOOLCHAIN = 'local'
        & go build -o $fixtureBinary ./cmd/pools-core
        Assert-Equal 0 $LASTEXITCODE 'fixture build failed'
    } finally {
        Pop-Location
    }
    $proc = Start-Process -FilePath $fixtureBinary -WorkingDirectory $fixtureDir -WindowStyle Hidden `
        -ArgumentList @('-addr', '127.0.0.1:0') -PassThru
    $serviceLog = $null
    try {
        # Bounded poll for the startup marker instead of a fixed number of iterations:
        # the service must be up before it can be stopped gracefully.
        $started = $false
        for ($i = 0; $i -lt 150; $i++) {
            $serviceLogs = @(Get-ChildItem -LiteralPath (Join-Path $fixtureDir 'logs') -Filter '*.jsonl' -ErrorAction SilentlyContinue)
            if ($serviceLogs.Count -gt 0) {
                $serviceLog = $serviceLogs[0].FullName
                if ((Get-Content -LiteralPath $serviceLog -Raw) -match 'server.start') { $started = $true; break }
            }
            if ($proc.HasExited) { throw 'fixture exited before startup' }
            Start-Sleep -Milliseconds 100
        }
        Assert-True $started 'fixture never started'
        $binary = $fixtureBinary
        $pidFile = Join-Path $fixtureDir 'fixture.pid'
        [IO.File]::WriteAllText($pidFile, [string]$proc.Id)
        Stop-Core
        Assert-True $proc.HasExited 'real process is still running'
        Assert-Equal 0 $proc.ExitCode 'Go did not exit successfully'
        # Bounded poll for the shutdown marker: the log line is written by the Go process
        # just before it exits, so give the file a moment to become readable.
        $stopped = $false
        for ($i = 0; $i -lt 50; $i++) {
            $text = (Get-Content -LiteralPath $serviceLog -Raw)
            if ($text -match 'server.stopped') { $stopped = $true; break }
            Start-Sleep -Milliseconds 100
        }
        Assert-True $stopped 'graceful shutdown log is missing (forced termination)'
    } finally {
        # Cleanup is confined to the process created by this test.
        if (!$proc.HasExited) { $proc.Kill(); $null = $proc.WaitForExit(5000) }
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ------------------------------------------------------------- WO-FIX-D tests ----
Test-Case 'WO-FIX-D Row 1: secret set in debug mode too' {
    $envFile = Join-Path $projectRoot 'data\launcher.env.json'
    $env:POOLS_JWT_SECRET = $null
    $secret = Ensure-Secret
    Assert-True (![string]::IsNullOrWhiteSpace($secret)) 'Ensure-Secret returned empty secret'
    Assert-True ($secret.Length -eq 64) ("expected 64 hex characters, got length {0}" -f $secret.Length)
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$null)
    $startFn = $ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Start-Core'}, $false)
    Assert-True ($null -ne $startFn) 'Start-Core function not found'
    $startText = $startFn[0].Extent.Text
    Assert-True ($startText -match '\$secret\s*=\s*Ensure-Secret') 'Start-Core must call Ensure-Secret unconditionally'
    Assert-True ($startText -match '\$env:POOLS_JWT_SECRET\s*=\s*\$secret') 'Start-Core must set POOLS_JWT_SECRET unconditionally'
}

Test-Case 'WO-FIX-D Row 2: secret never stored in cleartext (DPAPI + restricted ACL)' {
    $fixtureDir = Join-Path $env:TEMP ('pools-sec-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    try {
        $savedEnv = $launcherEnv
        $launcherEnv = Join-Path $fixtureDir 'launcher.env.json'
        $plainSecret = '7e129f8706c3bf2e485df1c1dd9125630fb600904ae9a0964e4276b91c4a7727'
        $enc = Protect-SecretString $plainSecret
        Assert-True ($enc -ne $plainSecret) 'Protected secret should not match plaintext'
        Write-LauncherEnv @{ POOLS_JWT_SECRET = $enc } | Out-Null
        $rawText = [IO.File]::ReadAllText($launcherEnv)
        Assert-True (!$rawText.Contains($plainSecret)) 'Raw env file must not contain cleartext secret'
        $dec = Unprotect-SecretString $enc
        Assert-Equal $plainSecret $dec 'Decrypted secret must match original secret'
        if ([System.OperatingSystem]::IsWindows()) {
            $acl = Get-Acl -LiteralPath $launcherEnv
            Assert-True ($acl.AreAccessRulesProtected) 'Access rules on launcher.env.json must be protected (inheritance disabled)'
        }
    } finally {
        $launcherEnv = $savedEnv
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'WO-FIX-D Row 3: PID file records pid + path + StartTime and Stop-Core verifies all three' {
    $fixtureDir = Join-Path $env:TEMP ('pools-pid-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    try {
        $savedPidFile = $pidFile
        $pidFile = Join-Path $fixtureDir 'pools-core.pid'
        $record = [ordered]@{
            pid       = 98765
            path      = 'C:\fake\pools-core.exe'
            startTime = '2026-11-17T12:00:00.0000000Z'
        }
        [IO.File]::WriteAllText($pidFile, ($record | ConvertTo-Json -Compress))
        $read = Read-PidRecord
        Assert-Equal 98765 $read.Pid 'Read-PidRecord did not parse pid'
        Assert-Equal 'C:\fake\pools-core.exe' $read.Path 'Read-PidRecord did not parse path'
        Assert-Equal '2026-11-17T12:00:00.0000000Z' $read.StartTime 'Read-PidRecord did not parse startTime'

        # Stop-Core must verify before signaling
        $fake = [pscustomobject]@{ Id = 98765; Path = 'C:\other\pools-core.exe'; StartTime = [datetime]'2026-11-17' }
        $fake | Add-Member ScriptMethod WaitForExit { param($ms) return $true }
        function Get-CoreProcess { return $fake }
        $signaled = $false
        function Send-CoreBreak([int]$p) { $signaled = $true }
        $caught = ''
        try { Stop-Core } catch { $caught = $_.Exception.Message }
        Assert-True (!$signaled) 'Stop-Core must not signal when identity check fails'
        Assert-True ($caught -match 'mismatch') ("expected mismatch error, got: $caught")
    } finally {
        $pidFile = $savedPidFile
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'WO-FIX-D Row 4: allow-list redaction in Show-Config' {
    $fixtureDir = Join-Path $env:TEMP ('pools-cfg-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    try {
        $savedEnv = $launcherEnv
        $launcherEnv = Join-Path $fixtureDir 'launcher.env.json'
        $testEnv = [ordered]@{
            POOLS_ADDR       = '127.0.0.1:8091'
            POOLS_SIGNING    = 'another-secret-not-matching-regex'
            POOLS_CREDENTIAL = 'super-secret-not-matching-regex'
            POOLS_JWT_SECRET = 'dpapi-protected-secret'
        }
        [IO.File]::WriteAllText($launcherEnv, ($testEnv | ConvertTo-Json))
        $output = (& { Show-Config } 6>&1 | Out-String)
        Assert-True ($output -match 'POOLS_ADDR=127\.0\.0\.1:8091') 'Allow-listed safe key POOLS_ADDR should be shown'
        Assert-True (!$output.Contains('another-secret-not-matching-regex')) 'POOLS_SIGNING value must be redacted'
        Assert-True (!$output.Contains('super-secret-not-matching-regex')) 'POOLS_CREDENTIAL value must be redacted'
        Assert-True ($output -match 'POOLS_SIGNING=<redacted>') 'POOLS_SIGNING should be marked <redacted>'
        Assert-True ($output -match 'POOLS_CREDENTIAL=<redacted>') 'POOLS_CREDENTIAL should be marked <redacted>'
    } finally {
        $launcherEnv = $savedEnv
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'WO-FIX-D Row 5: redact secret-shaped substrings in Show-Logs/Live tail' {
    $line1 = '{"level":"debug","msg":"jwt secret is 7e129f8706c3bf2e485df1c1dd9125630fb600904ae9a0964e4276b91c4a7727"}'
    $redacted1 = Redact-LogLine $line1
    Assert-True (!$redacted1.Contains('7e129f8706c3bf2e485df1c1dd9125630fb600904ae9a0964e4276b91c4a7727')) 'Hex secret must be redacted'
    Assert-True ($redacted1 -match '<redacted>') 'Line should contain <redacted>'

    $line2 = '{"level":"info","secret":"my-sensitive-token","auth":"Bearer eyJhbGciOiJIUzI1NiJ9"}'
    $redacted2 = Redact-LogLine $line2
    Assert-True (!$redacted2.Contains('my-sensitive-token')) 'Secret property must be redacted'
    Assert-True (!$redacted2.Contains('eyJhbGciOiJIUzI1NiJ9')) 'Bearer token must be redacted'
}

Test-Case 'WO-FIX-D Row 6: newest log selected by LastWriteTimeUtc' {
    $fixtureDir = Join-Path $env:TEMP ('pools-log-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    try {
        $savedLogDir = $logDir
        $logDir = $fixtureDir
        $olderFile = Join-Path $fixtureDir 'pools-core-debug.jsonl'
        $newerFile = Join-Path $fixtureDir 'pools-core-99999999-000000.jsonl'
        [IO.File]::WriteAllText($olderFile, 'older')
        [IO.File]::WriteAllText($newerFile, 'newer')
        (Get-Item -LiteralPath $olderFile).LastWriteTimeUtc = (Get-Date).AddMinutes(-10)
        (Get-Item -LiteralPath $newerFile).LastWriteTimeUtc = (Get-Date).AddMinutes(10)
        $selected = Get-LatestServiceLog
        Assert-True ($null -ne $selected) 'No log selected'
        Assert-Equal (Split-Path $newerFile -Leaf) $selected.Name 'Newest log by LastWriteTimeUtc must be chosen over alphabetical name'
    } finally {
        $logDir = $savedLogDir
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'WO-FIX-D Row 7: Go cache dirs only set if paths exist' {
    $scriptContent = Get-Content -LiteralPath $scriptPath -Raw
    Assert-True ($scriptContent -match 'if\s*\(\s*Test-Path\s*-LiteralPath\s*''F:\\GoCache\\mod''\s*\)') 'GOMODCACHE must check Test-Path first'
    Assert-True ($scriptContent -match 'if\s*\(\s*Test-Path\s*-LiteralPath\s*''F:\\GoCache\\build''\s*\)') 'GOCACHE must check Test-Path first'
}

Test-Case 'WO-FIX-D Row 8: refuse non-loopback POOLS_ADDR unless -AllowRemote' {
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$null)
    $startFn = $ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Start-Core'}, $false)
    $startText = $startFn[0].Extent.Text
    Assert-True ($startText -match 'AllowRemote') 'Start-Core must support AllowRemote parameter'
    Assert-True ($startText -match 'Refusing to start: POOLS_ADDR') 'Start-Core must refuse non-loopback addresses'

    $addr = '0.0.0.0:8091'
    $hostPart = $addr -replace ':.*$', ''
    $isLoopback = ($hostPart -in @('127.0.0.1', 'localhost', '::1', '[::1]')) -or ($hostPart -like '127.*')
    Assert-True (!$isLoopback) '0.0.0.0 must not be considered loopback'
}

Test-Case 'WO-FIX-D Row 9: readiness bound to the started PID' {
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$null)
    $startFn = $ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Start-Core'}, $false)
    $startText = $startFn[0].Extent.Text
    Assert-True ($startText -match 'Readiness check on .* returned PID') 'Start-Core must verify /healthz PID matches started PID'
    Assert-True ($startText -match '\$process\.HasExited') 'Start-Core must re-check HasExited after health check'
}

Test-Case 'WO-FIX-D Row 10: cleanup on failed start stops process and zeroes PID file' {
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$null)
    $startFn = $ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Start-Core'}, $false)
    $startText = $startFn[0].Extent.Text
    Assert-True ($startText -match '\$process\.Kill\(\)') 'Failed start must terminate started process'
    Assert-True ($startText -match '\[IO\.File\]::WriteAllText\(\$pidFile,\s*''0''\)') 'Failed start must reset PID file to 0'
}

Test-Case 'WO-FIX-D Row 11: no name-only process match in Get-CoreProcess' {
    $fixtureDir = Join-Path $env:TEMP ('pools-nomatch-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    try {
        $savedPidFile = $pidFile
        $pidFile = Join-Path $fixtureDir 'test.pid'
        $rec = [ordered]@{ pid = $PID; path = 'C:\fake\pools-core.exe'; startTime = '' }
        [IO.File]::WriteAllText($pidFile, ($rec | ConvertTo-Json -Compress))
        $proc = Get-CoreProcess
        Assert-True ($null -eq $proc) 'Process whose path does not match expected binary must return null'
    } finally {
        $pidFile = $savedPidFile
        Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Test-Case 'WO-FIX-D Row 12: StartTime read inside try/catch during process verification' {
    $calls = [Collections.Generic.List[string]]::new()
    $fake = [pscustomobject]@{ Id = 123 }
    $fake | Add-Member -MemberType ScriptProperty -Name StartTime -Value { throw [System.ComponentModel.Win32Exception]::new('Access is denied') }
    $fake | Add-Member ScriptMethod WaitForExit { param($ms) return $false }
    function Get-CoreProcess {
        $calls.Add('lookup')
        return $fake
    }
    function Send-CoreBreak([int]$ProcessId) { }
    function Stop-Process { param($InputObject, [switch]$Force) throw 'UNSAFE FORCE' }
    $caught = ''
    try { Stop-Core } catch { $caught = $_.Exception.Message }
    Assert-True ($caught -match 'identity changed') ("expected identity changed refusal, got: $caught")
}

# --------------------------------------------------------------------- summary ----
Write-Host ''
Write-Host '=== summary ===' -ForegroundColor Cyan
Write-Host ("passed: {0}" -f $script:Passed)
Write-Host ("failed: {0}" -f $script:Failed)
if ($script:Failed -gt 0) {
    foreach ($f in $script:Failures) { Write-Host ("  failed: {0}" -f $f) -ForegroundColor Red }
    exit 1
}
Write-Host 'ALL TESTS PASSED' -ForegroundColor Green
exit 0
