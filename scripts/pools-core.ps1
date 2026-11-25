# Path: scripts/pools-core.ps1 | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
# What/why: PowerShell 7 lifecycle launcher + one-screen TUI menu for pools-core (WO-T5, WWPP-R41.1).
#          Builds when sources are newer, starts/stops/restarts, tails JSON logs, selects the
#          store backend, and logs every launcher action to logs/launcher.jsonl. No global install.
# WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the monitor/admin links now open the real
#   /pools/monitor.html page served by pools-core, not the old /web/ placeholders.
#Requires -Version 7.0
param(
    [ValidateSet('menu','start','start-debug','start-normal','stop','restart','status','logs','tail','config','store','monitor','admin','diagnostics','test','quit')]
    [string]$Action = 'menu',
    [string]$Level = '',
    [int]$Tail = 40,
    [string]$Store = '',
    [switch]$OpenMonitor,
    [switch]$OpenAdmin,
    [switch]$AllowRemote
)

$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------- layout ----
$projectRoot  = Split-Path $PSScriptRoot -Parent
Set-Location -LiteralPath $projectRoot
if (Test-Path -LiteralPath 'F:\GoCache\mod') { $env:GOMODCACHE = 'F:\GoCache\mod' }
if (Test-Path -LiteralPath 'F:\GoCache\build') { $env:GOCACHE = 'F:\GoCache\build' }
$env:GOTOOLCHAIN = 'local'

$binary      = Join-Path $projectRoot 'bin\pools-core.exe'
$binDir      = Join-Path $projectRoot 'bin'
$dataDir     = Join-Path $projectRoot 'data'
$logDir      = Join-Path $projectRoot 'logs'
$pidFile     = Join-Path $dataDir 'pools-core.pid'
$launcherEnv = Join-Path $dataDir 'launcher.env.json'
$launcherLog = Join-Path $logDir 'launcher.jsonl'
$configFile  = Join-Path $projectRoot 'config.example.json'
$defaultAddr = '127.0.0.1:8091'

# Backend reality check (WO-T5): the Go skeleton only implements sqlite; pg is an explicit stub.
$storeCatalog = @(
    [ordered]@{ Name='sqlite';   Driver='sqlite'; Status='implemented'; Note='modernc SQLite file (default data/pools.db)' }
    [ordered]@{ Name='postgres'; Driver='pg';     Status='stub';        Note='pgx seam only; store.Open returns not-implemented' }
    [ordered]@{ Name='json';     Driver=$null;    Status='missing';     Note='no JSON store adapter in this build' }
    [ordered]@{ Name='csv';      Driver=$null;    Status='missing';     Note='no CSV store adapter in this build' }
    [ordered]@{ Name='redis';    Driver=$null;    Status='missing';     Note='no Redis store adapter in this build' }
)

# ------------------------------------------------------------- utilities ----
function Ensure-Layout {
    foreach ($d in @($binDir, $dataDir, $logDir)) {
        if (!(Test-Path -LiteralPath $d)) { New-Item -ItemType Directory -Force -Path $d | Out-Null }
    }
}

function Protect-FileAcl([string]$Path) {
    if (![System.OperatingSystem]::IsWindows()) { return }
    if (!(Test-Path -LiteralPath $Path)) { return }
    try {
        $acl = New-Object System.Security.AccessControl.FileSecurity
        $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $currentUser,
            [System.Security.AccessControl.FileSystemRights]::FullControl,
            [System.Security.AccessControl.AccessControlType]::Allow
        )
        $acl.SetAccessRuleProtection($true, $false)
        $acl.SetAccessRule($rule)
        Set-Acl -LiteralPath $Path -AclObject $acl
    } catch {
        Write-Warning "Could not restrict ACL on $Path : $($_.Exception.Message)"
    }
}

function Protect-SecretString([string]$PlainSecret) {
    if ([string]::IsNullOrWhiteSpace($PlainSecret)) { return '' }
    if ([System.OperatingSystem]::IsWindows()) {
        try {
            $sec = ConvertTo-SecureString -String $PlainSecret -AsPlainText -Force
            return (ConvertFrom-SecureString -SecureString $sec)
        } catch {
            return $PlainSecret
        }
    }
    return $PlainSecret
}

function Unprotect-SecretString([string]$EncryptedSecret) {
    if ([string]::IsNullOrWhiteSpace($EncryptedSecret)) { return '' }
    if ([System.OperatingSystem]::IsWindows()) {
        try {
            $sec = ConvertTo-SecureString -String $EncryptedSecret -ErrorAction Stop
            $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
            $plain = [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
            [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
            return $plain
        } catch {
            return $EncryptedSecret
        }
    }
    return $EncryptedSecret
}

function Read-LauncherEnv {
    $result = [ordered]@{}
    if (![string]::IsNullOrWhiteSpace($launcherEnv) -and (Test-Path -LiteralPath $launcherEnv)) {
        try {
            $parsed = Get-Content -LiteralPath $launcherEnv -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
            foreach ($k in @($parsed.Keys)) { $result[$k] = $parsed[$k] }
        } catch {
            Write-Host "Warning: $launcherEnv is not readable JSON; ignoring it." -ForegroundColor Yellow
        }
    }
    return ,$result
}

function Write-LauncherEnv([hashtable]$Updates) {
    Ensure-Layout
    $current = Read-LauncherEnv
    foreach ($k in @($Updates.Keys)) { $current[$k] = $Updates[$k] }
    $current['_meta']    = 'data/launcher.env.json | hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 | launcher environment; scripts/pools-core.ps1 start reads the POOLS_* keys'
    $current['updatedAt'] = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    $tmp = "$launcherEnv.tmp"
    [IO.File]::WriteAllText($tmp, (($current | ConvertTo-Json -Depth 6) + "`n"))
    Move-Item -LiteralPath $tmp -Destination $launcherEnv -Force
    Protect-FileAcl $launcherEnv
    return ,$current
}

function Write-LauncherLog {
    param([string]$Action, [string]$Result = 'ok', [double]$Ms = 0, [string]$Detail = '')
    try {
        Ensure-Layout
        $entry = [ordered]@{
            ts     = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
            action = $Action
            result = $Result
            ms     = [math]::Round($Ms, 1)
        }
        if ($Detail) { $entry['detail'] = $Detail }
        [IO.File]::AppendAllText($launcherLog, (($entry | ConvertTo-Json -Compress) + "`n"))
    } catch {
        Write-Host "Warning: could not write launcher log: $($_.Exception.Message)" -ForegroundColor Yellow
    }
}

function Get-EffectiveAddress {
    if ($env:POOLS_ADDR) { return $env:POOLS_ADDR }
    $e = Read-LauncherEnv
    if ($e['POOLS_ADDR']) { return [string]$e['POOLS_ADDR'] }
    if (Test-Path -LiteralPath $configFile) {
        try {
            $c = Get-Content -LiteralPath $configFile -Raw | ConvertFrom-Json -ErrorAction Stop
            if ($c.addr) { return [string]$c.addr }
        } catch { }
    }
    return $defaultAddr
}

function Get-BrowserAddress([string]$Address) {
    if ($Address -match '^:\d+$') { return '127.0.0.1' + $Address }
    if ($Address -match '^(0\.0\.0\.0|\[::\]|::):') { return $Address -replace '^(0\.0\.0\.0|\[::\]|::):', '127.0.0.1:' }
    return $Address
}

function Get-CurrentStore {
    $e = Read-LauncherEnv
    if ($e['POOLS_STORE']) { return [string]$e['POOLS_STORE'] }
    return 'sqlite'
}

function Get-CurrentMode {
    $e = Read-LauncherEnv
    $lvl = if ($e['POOLS_LOG_LEVEL']) { [string]$e['POOLS_LOG_LEVEL'] } else { '' }
    $dbg = if ($e['POOLS_DEBUG']) { [string]$e['POOLS_DEBUG'] } else { '' }
    if ($lvl -eq '' -and $dbg -eq '') { return 'debug' }
    if ($lvl -eq 'debug' -or $dbg -eq '1' -or $dbg -eq 'true') { return 'debug' }
    return 'normal'
}

function Get-StoreEntry([string]$Name) {
    foreach ($s in $storeCatalog) { if ($s.Name -eq $Name) { return $s } }
    return $null
}

function Read-PidRecord {
    if ([string]::IsNullOrWhiteSpace($pidFile) -or !(Test-Path -LiteralPath $pidFile)) { return $null }
    $raw = Get-Content -LiteralPath $pidFile -Raw -ErrorAction SilentlyContinue
    if ([string]::IsNullOrWhiteSpace($raw)) { return $null }
    $raw = $raw.Trim()
    if ($raw -eq '0') { return $null }
    try {
        $obj = $raw | ConvertFrom-Json -ErrorAction Stop
        if ($null -ne $obj -and $obj.pid) {
            $st = ''
            if ($obj.startTime -is [datetime]) {
                $st = $obj.startTime.ToUniversalTime().ToString('o')
            } elseif ($obj.startTime) {
                $st = [string]$obj.startTime
            }
            return [ordered]@{
                Pid       = [int]$obj.pid
                Path      = $(if ($obj.path) { [string]$obj.path } else { '' })
                StartTime = $st
            }
        }
    } catch { }
    $intPid = 0
    if ([int]::TryParse($raw, [ref]$intPid) -and $intPid -gt 0) {
        return [ordered]@{ Pid = $intPid; Path = ''; StartTime = '' }
    }
    return $null
}

function Get-CoreProcess {
    $record = Read-PidRecord
    if ($null -eq $record -or $record.Pid -le 0) { return $null }
    $proc = Get-Process -Id $record.Pid -ErrorAction SilentlyContinue
    if ($null -eq $proc) { return $null }
    $path = $null
    try { $path = $proc.Path } catch { $path = $null }
    if (!$path) { return $null }
    $expectedPath = if ($record.Path) { $record.Path } else { $binary }
    if ($path -ne $expectedPath) { return $null }
    if ($record.StartTime) {
        $st = $null
        try { $st = $proc.StartTime.ToUniversalTime().ToString('o') } catch { $st = $null }
        if ($null -eq $st -or $st -ne $record.StartTime) { return $null }
    }
    return $proc
}

function Get-Health([string]$Address) {
    try {
        $resp = Invoke-RestMethod -Uri "http://$Address/healthz" -TimeoutSec 2 -ErrorAction Stop
        return [ordered]@{ Ok = $true; Raw = ($resp | ConvertTo-Json -Compress); Data = $resp }
    } catch {
        return [ordered]@{ Ok = $false; Raw = $_.Exception.Message; Data = $null }
    }
}

function Get-CoreState {
    $addr = Get-EffectiveAddress
    $proc = Get-CoreProcess
    $state = [ordered]@{
        Running = ($null -ne $proc)
        Process = $proc
        Pid     = 0
        Addr    = $addr
        Uptime  = ''
        Health  = $null
    }
    if ($proc) {
        $state.Pid = $proc.Id
        try { $state.Uptime = ($proc.StartTime).ToString('yyyy-MM-dd HH:mm:ss') + ' (' + ((Get-Date) - $proc.StartTime).ToString('d\.hh\:mm\:ss') + ')' }
        catch { $state.Uptime = 'unknown' }
        $state.Health = Get-Health $addr
    }
    return ,$state
}

function Get-LatestServiceLog {
    if (!(Test-Path -LiteralPath $logDir)) { return $null }
    $latest = Get-ChildItem -LiteralPath $logDir -Filter 'pools-core-*.jsonl' -ErrorAction SilentlyContinue |
              Sort-Object LastWriteTimeUtc | Select-Object -Last 1
    return $latest
}

function Redact-LogLine([string]$Line) {
    if ([string]::IsNullOrWhiteSpace($Line)) { return $Line }
    $r = $Line
    $r = [regex]::Replace($r, 'Bearer\s+[A-Za-z0-9\-_\.]+', 'Bearer <redacted>', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
    $r = [regex]::Replace($r, '("?(?:secret|jwt|token|password|credential|signing|key)"?\s*[:=]\s*)"?[^",\s}]+"?', '$1"<redacted>"', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
    $r = [regex]::Replace($r, '(?i)\b(jwt(?:\s+secret)?|secret|token|password|credential|signing|key)\s*(is|[:=])\s*([^\s",}]+)', '$1 $2 <redacted>')
    $r = [regex]::Replace($r, '\b[0-9a-fA-F]{32,64}\b', '<redacted>')
    return $r
}

function Get-LogLevel([string]$Line) {
    try {
        $obj = $Line | ConvertFrom-Json -ErrorAction Stop
        if ($null -ne $obj -and ($obj.PSObject.Properties.Name -contains 'level')) {
            return ([string]$obj.level).ToLowerInvariant()
        }
    } catch { }
    return ''
}

function Test-LogLevel([string]$Line, [string]$Filter) {
    if ([string]::IsNullOrWhiteSpace($Filter)) { return $true }
    $ranks = @{ 'debug' = 0; 'info' = 1; 'warn' = 2; 'warning' = 2; 'error' = 3 }
    $want = $Filter.ToLowerInvariant()
    if (!$ranks.ContainsKey($want)) { return $true }
    $lvl = Get-LogLevel $Line
    if (!$ranks.ContainsKey($lvl)) { return $false }
    return ($ranks[$lvl] -ge $ranks[$want])
}

# ------------------------------------------------------------- lifecycle ----
function Test-SourcesNewer {
    if (!(Test-Path -LiteralPath $binary)) { return $true }
    $built = (Get-Item -LiteralPath $binary).LastWriteTimeUtc
    $inputs = @('cmd', 'internal', 'web', 'go.mod', 'go.sum')
    $newer = Get-ChildItem -LiteralPath $inputs -Recurse -File -ErrorAction SilentlyContinue |
             Where-Object { $_.LastWriteTimeUtc -gt $built } | Select-Object -First 1
    return ($null -ne $newer)
}

function Build-Core {
    Write-Host 'Building pools-core (go build -o bin\pools-core.exe ./cmd/pools-core) ...' -ForegroundColor DarkGray
    & go build -o $binary ./cmd/pools-core
    if ($LASTEXITCODE -ne 0) { throw 'Build failed (go build).' }
}

function Ensure-Secret {
    if ($env:POOLS_JWT_SECRET) { return $env:POOLS_JWT_SECRET }
    $e = Read-LauncherEnv
    if ($e['POOLS_JWT_SECRET']) {
        $decrypted = Unprotect-SecretString ([string]$e['POOLS_JWT_SECRET'])
        if ($decrypted) { return $decrypted }
    }
    $bytes = New-Object byte[] 32
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    $secret = [Convert]::ToHexString($bytes).ToLowerInvariant()
    $enc = Protect-SecretString $secret
    Write-LauncherEnv @{ POOLS_JWT_SECRET = $enc } | Out-Null
    Write-Host 'Generated local DPAPI-protected jwt secret in data\launcher.env.json (value not shown).' -ForegroundColor Yellow
    return $secret
}

function Ensure-NormalSecret {
    return (Ensure-Secret)
}

function Start-Core([string]$Mode, [switch]$AllowRemoteParam = $AllowRemote) {
    if (Get-CoreProcess) { Write-Host 'Already running.' -ForegroundColor Yellow; return }
    Ensure-Layout
    if (Test-SourcesNewer) { Build-Core }

    # Store backend -> driver (only what the Go build actually implements).
    $store = Get-CurrentStore
    $entry = Get-StoreEntry $store
    if ($null -eq $entry) { throw "Unknown store backend '$store' in data\launcher.env.json." }
    if ($entry.Status -eq 'missing') {
        throw "Store backend '$store' is selected but not implemented by this Go build (implemented: sqlite; stub: postgres). Pick sqlite or add the adapter."
    }
    if ($entry.Status -eq 'stub') {
        Write-Warning "The PostgreSQL adapter is a skeleton: the service will exit with 'not implemented'."
    }
    $env:POOLS_DRIVER = $entry.Driver

    # Row 1: Secret set in both debug and normal modes
    $secret = Ensure-Secret
    $env:POOLS_JWT_SECRET = $secret

    if ($Mode -eq 'debug') {
        $env:POOLS_LOG_LEVEL = 'debug'
        $env:POOLS_DEBUG = '1'
    } else {
        $env:POOLS_LOG_LEVEL = 'info'
        $env:POOLS_DEBUG = '0'
    }

    $address = Get-EffectiveAddress
    # Row 8: warn/refuse non-loopback POOLS_ADDR unless -AllowRemote
    $hostPart = $address
    if ($address -match '^([^:]+):') { $hostPart = $Matches[1] }
    elseif ($address -match '^:(\d+)$') { $hostPart = '127.0.0.1' }

    $isLoopback = ($hostPart -in @('127.0.0.1', 'localhost', '::1', '[::1]')) -or ($hostPart -like '127.*')
    if (!$isLoopback) {
        if (!$AllowRemoteParam) {
            throw "Refusing to start: POOLS_ADDR '$address' is not a loopback address. Use -AllowRemote to permit non-loopback binding."
        } else {
            Write-Warning "Binding non-loopback address '$address' because -AllowRemote was specified."
        }
    }

    $env:POOLS_ADDR = $address
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $stdout = Join-Path $logDir "launcher-$stamp.stdout.log"
    $stderr = Join-Path $logDir "launcher-$stamp.stderr.log"
    $args = @()
    if (Test-Path -LiteralPath $configFile) { $args += @('-config', 'config.example.json') }
    $process = Start-Process -FilePath $binary -WorkingDirectory $projectRoot -WindowStyle Hidden `
                            -RedirectStandardOutput $stdout -RedirectStandardError $stderr `
                            -ArgumentList $args -PassThru

    # Row 3: Record PID + exe path + StartTime
    $startTimeStr = ''
    try { $startTimeStr = $process.StartTime.ToUniversalTime().ToString('o') } catch { }
    $pidRecord = [ordered]@{
        pid       = $process.Id
        path      = $binary
        startTime = $startTimeStr
    }
    [IO.File]::WriteAllText($pidFile, ($pidRecord | ConvertTo-Json -Compress))

    try {
        $ready = $false
        for ($i = 0; $i -lt 50; $i++) {
            if ($process.HasExited) { throw "Service exited with code $($process.ExitCode). See logs\launcher-$stamp.stderr.log" }
            $health = Get-Health $address
            if ($health.Ok) {
                # Row 9: readiness bound to the started PID
                $hPid = $null
                if ($health.Data) {
                    if ($health.Data.PSObject.Properties['pid']) { $hPid = [int]$health.Data.pid }
                    elseif ($health.Data.PSObject.Properties['PID']) { $hPid = [int]$health.Data.PID }
                }
                if ($null -ne $hPid -and $hPid -gt 0 -and $hPid -ne $process.Id) {
                    throw "Readiness check on $address returned PID $hPid, but started process has PID $($process.Id); port conflict."
                }
                if ($process.HasExited) {
                    throw "Service exited with code $($process.ExitCode) after health check. See logs\launcher-$stamp.stderr.log"
                }
                $ready = $true
                break
            }
            Start-Sleep -Milliseconds 200
        }
        if (!$ready) { throw "Health check timed out for $address; inspect logs\launcher-$stamp.stderr.log" }
        if ($process.HasExited) { throw "Service exited with code $($process.ExitCode). See logs\launcher-$stamp.stderr.log" }
    } catch {
        # Row 10: cleanup on failed start
        try {
            if (!$process.HasExited) {
                $process.Kill()
                $null = $process.WaitForExit(3000)
            }
        } catch { }
        if (Test-Path -LiteralPath $pidFile) {
            [IO.File]::WriteAllText($pidFile, '0')
        }
        throw
    }

    Write-LauncherEnv @{ POOLS_LOG_LEVEL = $env:POOLS_LOG_LEVEL; POOLS_DEBUG = $env:POOLS_DEBUG; POOLS_STORE = $store; POOLS_ADDR = $address } | Out-Null

    Write-Host "Started pools-core (PID $($process.Id))." -ForegroundColor Green
    Write-Host ("Mode      : {0}  (POOLS_LOG_LEVEL={1}, POOLS_DEBUG={2})" -f $Mode.ToUpperInvariant(), $env:POOLS_LOG_LEVEL, $env:POOLS_DEBUG)
    Write-Host ("Store     : {0}  (driver={1})" -f $store, $entry.Driver)
    Write-Host ("Listen    : http://{0}" -f (Get-BrowserAddress $address))
    Write-Host ("Monitor   : http://{0}/pools/monitor.html" -f (Get-BrowserAddress $address))
    Write-Host ("Logs      : logs\launcher-{0}.stdout.log" -f $stamp)
    if ($OpenMonitor) { Open-Page "$((Get-BrowserAddress $address))/pools/monitor.html" }
}

# Review-01 (Codex, 2026-11-17): isolate console attachment in a hidden helper.
# CTRL_BREAK reaches Go's os.Interrupt handler; never broadcast into a shared console.
# WO-T24 (hermes MiniMax-M3, 2026-11-17): the helper used to compile this C# shim with
# Add-Type -TypeDefinition on the signal path. That Roslyn compile costs ~0.5 s warm and
# ~6.5 s cold on this machine, so the old 5 s helper budget killed the helper BEFORE it
# could deliver CTRL_BREAK: Go never saw SIGINT, the 12 s graceful wait expired, the
# process was force killed (exit code -1) and the launcher test failed with
# "Go did not exit successfully". The shim is now compiled once into a content-hashed
# assembly cache before the signal and the helper only loads it (~50 ms), so the signal
# path never races a compiler warm-up any more.

# The P/Invoke shim that signals an isolated service console. Kept verbatim except for
# the comments; it is compiled once and cached, then loaded by the helper.
function Get-CoreConsoleSource {
    return @'
using System;
using System.Runtime.InteropServices;
public static class CoreConsole {
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool FreeConsole();
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool AttachConsole(uint pid);
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint GetConsoleProcessList(uint[] ids, uint size);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GenerateConsoleCtrlEvent(uint evt, uint group);
    delegate bool Handler(uint evt);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetConsoleCtrlHandler(Handler handler, bool add);
    static Handler ignore = evt => true;
    public static void Send(uint pid) {
        FreeConsole();
        if (!AttachConsole(pid)) throw new System.ComponentModel.Win32Exception();
        try {
            uint[] ids = new uint[16];
            uint count = GetConsoleProcessList(ids, (uint)ids.Length);
            if (count == 0 || count > ids.Length) throw new Exception("Cannot verify isolated service console");
            uint self = (uint)System.Diagnostics.Process.GetCurrentProcess().Id;
            for (int i=0; i<count; i++)
                if (ids[i] != pid && ids[i] != self) throw new Exception("Refusing CTRL_BREAK into shared console");
            if (!SetConsoleCtrlHandler(ignore, true)) throw new System.ComponentModel.Win32Exception();
            if (!GenerateConsoleCtrlEvent(1, 0)) throw new System.ComponentModel.Win32Exception();
            System.Threading.Thread.Sleep(100);
        } finally { FreeConsole(); }
    }
}
'@
}

# Content-hashed cache location for the compiled shim: <hash>.cs (source) and <hash>.dll
# (assembly). The hash changes whenever the source changes, so a stale assembly is never
# reused and no cache invalidation logic is needed.
function Get-CoreConsoleHelperPaths {
    $dir = Join-Path ([IO.Path]::GetTempPath()) 'pools-core-console-helper'
    if (!(Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
    $source = Get-CoreConsoleSource
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $hash = ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($source)))).Replace('-', '').Substring(0, 16)
    } finally {
        $sha.Dispose()
    }
    $sourcePath = Join-Path $dir ("CoreConsole-$hash.cs")
    $assemblyPath = Join-Path $dir ("CoreConsole-$hash.dll")
    if (!(Test-Path -LiteralPath $sourcePath)) { [IO.File]::WriteAllText($sourcePath, $source) }
    return @{ Dir = $dir; Source = $sourcePath; Assembly = $assemblyPath }
}

# Compile the shim once, outside the signal path. Returns $true when the cached assembly
# is available for the helper to load. A failure here is not fatal: the helper falls back
# to compiling the shim itself.
function Ensure-CoreConsoleAssembly([hashtable]$Paths) {
    if (Test-Path -LiteralPath $Paths.Assembly) { return $true }
    $tmp = "$($Paths.Assembly).$PID.dll"
    try {
        if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
        Add-Type -TypeDefinition ([IO.File]::ReadAllText($Paths.Source)) -OutputAssembly $tmp -OutputType Library -ErrorAction Stop
        if (Test-Path -LiteralPath $tmp) { Move-Item -LiteralPath $tmp -Destination $Paths.Assembly -Force -ErrorAction Stop }
        return (Test-Path -LiteralPath $Paths.Assembly)
    } catch {
        return $false
    } finally {
        if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
    }
}

function Send-CoreBreak([int]$ProcessId) {
    $paths = Get-CoreConsoleHelperPaths
    [void](Ensure-CoreConsoleAssembly $paths)

    # The helper loads the cached assembly (fast path). If the assembly is missing or
    # unloadable it compiles the shim itself, so the helper never depends on the cache.
    $body = @'
$ErrorActionPreference = 'Stop'
function Import-CoreConsole {
    if ('CoreConsole' -as [type]) { return }
    if (Test-Path -LiteralPath $AssemblyPath) {
        try { Add-Type -Path $AssemblyPath -ErrorAction Stop } catch { }
        if ('CoreConsole' -as [type]) { return }
    }
    $source = [IO.File]::ReadAllText($SourcePath)
    try {
        $tmp = "$AssemblyPath.$PID.dll"
        if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
        Add-Type -TypeDefinition $source -OutputAssembly $tmp -OutputType Library -ErrorAction Stop
        if (Test-Path -LiteralPath $tmp) { Move-Item -LiteralPath $tmp -Destination $AssemblyPath -Force -ErrorAction Stop }
        Add-Type -Path $AssemblyPath -ErrorAction Stop
        if ('CoreConsole' -as [type]) { return }
    } catch { }
    Add-Type -TypeDefinition $source -ErrorAction Stop
}
Import-CoreConsole
try { [CoreConsole]::Send([uint]$TargetPid); Write-Output 'CTRL_BREAK_SENT'; exit 0 }
catch { Write-Output ("CTRL_BREAK_FAILED: " + $_.Exception.Message); exit 1 }
'@
    $quotedSource  = $paths.Source.Replace("'", "''")
    $quotedAssembly = $paths.Assembly.Replace("'", "''")
    $prelude = "`$TargetPid = $ProcessId`n`$SourcePath = '$quotedSource'`n`$AssemblyPath = '$quotedAssembly'`n"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($prelude + $body))
    $hostExecutable = (Get-Process -Id $PID).Path
    $outFile = Join-Path $paths.Dir "helper-$ProcessId.out.txt"
    $errFile = Join-Path $paths.Dir "helper-$ProcessId.err.txt"
    $sender = Start-Process -FilePath $hostExecutable -WindowStyle Hidden `
        -ArgumentList @('-NoProfile', '-NonInteractive', '-EncodedCommand', $encoded) `
        -RedirectStandardOutput $outFile -RedirectStandardError $errFile -PassThru
    # The helper loads a prebuilt assembly (~50 ms). The budget only has to cover the
    # fallback compile of a cold Roslyn (~7 s measured), so it is bounded and still
    # leaves the 12 s graceful window of Stop-Core intact.
    $helperBudgetMs = 10000
    if (!$sender.WaitForExit($helperBudgetMs)) {
        $sender.Kill()
        $null = $sender.WaitForExit(1000)
        Remove-Item -LiteralPath $outFile, $errFile -Force -ErrorAction SilentlyContinue
        throw "Console signal helper timed out after $helperBudgetMs ms."
    }
    $detail = ''
    try {
        if (Test-Path -LiteralPath $outFile) { $detail = [string](Get-Content -LiteralPath $outFile -Raw) }
    } catch { }
    Remove-Item -LiteralPath $outFile, $errFile -Force -ErrorAction SilentlyContinue
    if ($sender.ExitCode -ne 0) {
        $reason = ($detail -replace "`r?`n", ' ').Trim()
        if (!$reason) { $reason = "helper exit code $($sender.ExitCode)" }
        throw "Could not send CTRL_BREAK to an isolated service console ($reason)."
    }
}

function Stop-Core {
    $proc = Get-CoreProcess
    if (!$proc) {
        Write-Host 'Not running.' -ForegroundColor Yellow
        if (![string]::IsNullOrWhiteSpace($pidFile) -and (Test-Path -LiteralPath $pidFile)) { [IO.File]::WriteAllText($pidFile, '0') }
        return
    }

    # Row 3: Verify pid, path, and StartTime before signalling
    $record = Read-PidRecord
    if ($record) {
        if ($record.Pid -and $proc.Id -ne $record.Pid) {
            throw "Process PID mismatch before signal: expected $($record.Pid), got $($proc.Id)"
        }
        if ($record.Path) {
            $p = $null
            try { $p = $proc.Path } catch { }
            if ($p -and $p -ne $record.Path) {
                throw "Process path mismatch before signal: expected $($record.Path), got $p"
            }
        }
        if ($record.StartTime) {
            $st = $null
            try { $st = $proc.StartTime.ToUniversalTime().ToString('o') } catch { }
            if ($st -and $st -ne $record.StartTime) {
                throw "Process StartTime mismatch before signal: expected $($record.StartTime), got $st"
            }
        }
    }

    try { Send-CoreBreak $proc.Id } catch { Write-Warning $_.Exception.Message }
    # The Go shutdown budget is 10 seconds. Force only after a longer grace period.
    if (!$proc.WaitForExit(12000)) {
        $verified = Get-CoreProcess
        # Row 12: StartTime read inside try/catch
        $verifiedStart = $null
        try { $verifiedStart = $verified.StartTime } catch { $verifiedStart = $null }
        $procStart = $null
        try { $procStart = $proc.StartTime } catch { $procStart = $null }
        if (!$verified -or $verified.Id -ne $proc.Id -or $null -eq $verifiedStart -or $null -eq $procStart -or $verifiedStart -ne $procStart) {
            throw 'Process identity changed while waiting for shutdown; refusing force stop.'
        }
        Write-Warning 'Graceful shutdown timed out after 12 seconds; forcing the verified process to exit.'
        Stop-Process -InputObject $verified -Force
        if (!$proc.WaitForExit(5000)) { throw 'Process did not exit; keeping PID file for inspection.' }
    }
    if (![string]::IsNullOrWhiteSpace($pidFile)) { [IO.File]::WriteAllText($pidFile, '0') }
    Write-Host "Stopped the verified local process (PID $($proc.Id))." -ForegroundColor Green
}

function Show-Status {
    $state = Get-CoreState
    $addr = $state.Addr
    $port = 'unknown'
    if ($addr -match ':(\d+)$') { $port = $Matches[1] }
    Write-Host '--- pools-core status ---'
    Write-Host ("  PID      : {0}" -f $(if ($state.Running) { "$($state.Pid) (running)" } else { 'not running' }))
    Write-Host ("  Address  : {0}   (port {1})" -f $addr, $port)
    Write-Host ("  Uptime   : {0}" -f $(if ($state.Running) { $state.Uptime } else { '-' }))
    Write-Host ("  Mode     : {0}  (POOLS_LOG_LEVEL={1}, POOLS_DEBUG={2})" -f (Get-CurrentMode).ToUpperInvariant(),
        $(if ((Read-LauncherEnv)['POOLS_LOG_LEVEL']) { (Read-LauncherEnv)['POOLS_LOG_LEVEL'] } else { 'debug' }),
        $(if ((Read-LauncherEnv)['POOLS_DEBUG']) { (Read-LauncherEnv)['POOLS_DEBUG'] } else { '1' }))
    Write-Host ("  Store    : {0}" -f (Get-CurrentStore))
    if ($state.Running -and $state.Health) {
        Write-Host ("  /healthz : {0}" -f $(if ($state.Health.Ok) { $state.Health.Raw } else { "unreachable ($($state.Health.Raw))" }))
    } else {
        Write-Host '  /healthz : not reachable (service not running)'
    }
}

function Show-Logs([int]$Count) {
    $latest = Get-LatestServiceLog
    if (!$latest) { Write-Host 'No service log yet (logs\pools-core-*.jsonl).' -ForegroundColor Yellow; return }
    Write-Host "--- $($latest.Name) (last $Count lines) ---"
    Get-Content -LiteralPath $latest.FullName -Tail $Count | ForEach-Object {
        Write-Host (Redact-LogLine $_)
    }
}

function Invoke-LiveTail([string]$Filter, [int]$Count) {
    $latest = Get-LatestServiceLog
    if (!$latest) { Write-Host 'No service log yet (logs\pools-core-*.jsonl).' -ForegroundColor Yellow; return }
    Write-Host "--- live tail: $($latest.Name) ---" -ForegroundColor Cyan
    Write-Host '    Ctrl+C stops the tail and returns to the menu.' -ForegroundColor DarkGray
    try {
        Get-Content -LiteralPath $latest.FullName -Tail $Count -Wait -ErrorAction Stop | ForEach-Object {
            if (Test-LogLevel $_ $Filter) { Write-Host (Redact-LogLine $_) }
        }
    } catch [System.Management.Automation.PipelineStoppedException] {
        Write-Host ''
        Write-Host 'Log tail stopped (Ctrl+C).' -ForegroundColor DarkGray
    } catch {
        Write-Host ''
        Write-Host "Log tail stopped: $($_.Exception.Message)" -ForegroundColor DarkGray
    }
}

function Open-Page([string]$Url) {
    try {
        Start-Process $Url
        Write-Host "Opened $Url" -ForegroundColor Green
    } catch {
        Write-Host "Could not open a browser for $Url : $($_.Exception.Message)" -ForegroundColor Yellow
        Write-Host "Open it manually: $Url"
    }
}

function Show-Config {
    $e = Read-LauncherEnv
    $addr = Get-EffectiveAddress
    Write-Host '--- effective configuration ---'
    Write-Host ("  config file      : {0} {1}" -f $configFile, $(if (![string]::IsNullOrWhiteSpace($configFile) -and (Test-Path -LiteralPath $configFile)) { '(present)' } else { '(absent)' }))
    Write-Host ("  listen address   : {0}" -f $addr)
    Write-Host ("  database         : data\pools.db (driver from store selection)")
    Write-Host ("  store backend    : {0}" -f (Get-CurrentStore))
    Write-Host ("  log directory    : {0}" -f $logDir)
    Write-Host ("  service log      : {0}" -f $(if (Get-LatestServiceLog) { (Get-LatestServiceLog).Name } else { 'none yet' }))
    Write-Host ("  launcher log     : {0}" -f $launcherLog)
    Write-Host ("  launcher env file: {0}" -f $launcherEnv)
    Write-Host ("  binary           : {0} {1}" -f $binary, $(if (![string]::IsNullOrWhiteSpace($binary) -and (Test-Path -LiteralPath $binary)) { (Get-Item -LiteralPath $binary).LastWriteTime.ToString('yyyy-MM-dd HH:mm:ss') } else { '(not built)' }))
    Write-Host ("  mode             : {0}" -f (Get-CurrentMode).ToUpperInvariant())
    Write-Host '  launcher env (secrets redacted):'
    $safeKeys = @('POOLS_ADDR', 'POOLS_DEBUG', 'POOLS_LOG_LEVEL', 'POOLS_STORE', 'updatedAt')
    if (@($e.Keys).Count -eq 0) {
        Write-Host '    (empty)'
    } else {
        foreach ($k in @($e.Keys)) {
            if ($k -eq '_meta') { continue }
            $v = [string]$e[$k]
            if ($k -notin $safeKeys) { $v = '<redacted>' }
            Write-Host ("    {0}={1}" -f $k, $v)
        }
    }
    Write-Host '  store catalog:'
    foreach ($s in $storeCatalog) {
        Write-Host ("    {0,-9} {1,-12} {2}" -f $s.Name, $s.Status, $s.Note)
    }
}

function Select-Store([string]$Preset) {
    Write-Host '--- store backend selection ---'
    foreach ($s in $storeCatalog) {
        Write-Host ("  {0,-9} {1,-12} {2}" -f $s.Name, $s.Status, $s.Note)
    }
    Write-Host ("  current selection: {0} (written as POOLS_STORE into data\launcher.env.json)" -f (Get-CurrentStore))
    $choice = $Preset
    if ([string]::IsNullOrWhiteSpace($choice)) {
        $choice = Read-Host 'Select store backend (name, blank to keep current)'
    }
    if ([string]::IsNullOrWhiteSpace($choice)) { Write-Host 'Selection unchanged.'; return }
    $entry = Get-StoreEntry $choice.Trim().ToLowerInvariant()
    if ($null -eq $entry) { throw "Unknown store backend '$choice'. Valid names: $($storeCatalog.Name -join ', ')." }
    Write-LauncherEnv @{ POOLS_STORE = $entry.Name } | Out-Null
    Write-Host "Store backend set to $($entry.Name) (POOLS_STORE in data\launcher.env.json). The next start reads it." -ForegroundColor Green
    if ($entry.Status -eq 'missing') { Write-Warning "This Go build has no $($entry.Name) adapter; starting with it will be refused." }
    if ($entry.Status -eq 'stub')    { Write-Warning "The $($entry.Name) adapter is a skeleton; the service will exit with 'not implemented'." }
}

function Show-Diagnostics {
    Write-Host '--- diagnostics ---'
    Write-Host ("  host            : {0} {1}" -f [Environment]::OSVersion.Platform, $PSVersionTable.PSVersion.ToString())
    Write-Host ("  project root    : {0}" -f $projectRoot)
    $go = Get-Command go -ErrorAction SilentlyContinue
    if ($go) {
        Write-Host ("  go              : {0}" -f (& go version))
        & go env GOMODCACHE GOCACHE GOTOOLCHAIN GOOS GOARCH | ForEach-Object { Write-Host ("  go env          : {0}" -f $_) }
    } else {
        Write-Host '  go              : NOT FOUND on PATH (build/test will fail)'
    }
    Write-Host ("  binary          : {0}" -f $(if (Test-Path -LiteralPath $binary) { "$binary ($((Get-Item -LiteralPath $binary).Length) bytes)" } else { 'not built' }))
    Write-Host ("  sources newer   : {0}" -f (Test-SourcesNewer))
    $state = Get-CoreState
    Write-Host ("  pid file        : {0} -> {1}" -f $pidFile, $(if (Test-Path -LiteralPath $pidFile) { (Get-Content -LiteralPath $pidFile -Raw).Trim() } else { 'absent' }))
    Write-Host ("  process         : {0}" -f $(if ($state.Running) { "running (PID $($state.Pid))" } else { 'not running' }))
    Write-Host ("  health          : {0}" -f $(if ($state.Running -and $state.Health.Ok) { $state.Health.Raw } else { 'not reachable' }))
    $port = $null
    if ($state.Addr -match ':(\d+)$') { $port = [int]$Matches[1] }
    if ($port) {
        $listener = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
        Write-Host ("  port {0}        : {1}" -f $port, $(if ($listener) { "listening (pid $($listener.OwningProcess))" } else { 'not listening' }))
    }
    $latest = Get-LatestServiceLog
    Write-Host ("  service log     : {0}" -f $(if ($latest) { "$($latest.Name) ($((Get-Item -LiteralPath $latest.FullName).Length) bytes)" } else { 'none yet' }))
    Write-Host ("  launcher log    : {0} {1}" -f $launcherLog, $(if (Test-Path -LiteralPath $launcherLog) { "$((Get-Item -LiteralPath $launcherLog).Length) bytes" } else { 'not created yet' }))
    Write-Host ("  launcher env    : {0}" -f $launcherEnv)
    Write-Host ("  disk free       : {0}" -f ((Get-PSDrive -Name ((Get-Item -LiteralPath $projectRoot).PSDrive.Name)).Free / 1GB).ToString('0.0') + ' GB')
}

function Invoke-Test {
    Write-Host '--- build + test ---'
    & go build ./...
    if ($LASTEXITCODE -ne 0) { throw 'go build ./... failed.' }
    Write-Host 'go build ./...   : OK'
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet ./... failed.' }
    Write-Host 'go vet ./...     : OK'
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test ./... failed.' }
    Write-Host 'go test ./...    : OK'
}

# ------------------------------------------------------------- dispatcher ----
function Invoke-CoreAction([string]$Choice) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $result = 'ok'
    $detail = ''
    try {
        switch ($Choice) {
            'start'        { Start-Core (Get-CurrentMode) -AllowRemoteParam:$AllowRemote }
            'start-debug'  { Start-Core 'debug' -AllowRemoteParam:$AllowRemote }
            'start-normal' { Start-Core 'normal' -AllowRemoteParam:$AllowRemote }
            'stop'         { Stop-Core }
            'restart'      { Stop-Core; Start-Core (Get-CurrentMode) -AllowRemoteParam:$AllowRemote }
            'status'       { Show-Status }
            'logs'         { Show-Logs $(if ($Tail -gt 0) { $Tail } else { 60 }) }
            'tail'         { Invoke-LiveTail $Level $(if ($Tail -gt 0) { $Tail } else { 40 }) }
            'config'       { Show-Config }
            'store'        { Select-Store $Store }
            'monitor'      { Open-Page "$((Get-BrowserAddress (Get-EffectiveAddress)))/pools/monitor.html" }
            'admin'        { Open-Page "$((Get-BrowserAddress (Get-EffectiveAddress)))/pools/monitor.html" }
            'diagnostics'  { Show-Diagnostics }
            'test'         { Invoke-Test }
            default        { throw "Unknown action '$Choice'." }
        }
    } catch {
        $result = 'error'
        $detail = $_.Exception.Message
        throw
    } finally {
        $sw.Stop()
        Write-LauncherLog -Action $Choice -Result $result -Ms $sw.Elapsed.TotalMilliseconds -Detail $detail
    }
}

# -------------------------------------------------------------- menu loop ----
function Show-Menu {
    while ($true) {
        if (-not [Console]::IsOutputRedirected) { Clear-Host }
        Write-Host ''
        Write-Host '=============== pools-core launcher (WO-T5) ===============' -ForegroundColor Cyan
        Write-Host (" mode: {0}   store: {1}   listen: {2}" -f (Get-CurrentMode).ToUpperInvariant(), (Get-CurrentStore), (Get-EffectiveAddress))
        Write-Host ''
        Write-Host '  1) Start (debug)' -ForegroundColor Green
        Write-Host '       start with POOLS_LOG_LEVEL=debug + POOLS_DEBUG=1 (full debug logging)'
        Write-Host '  2) Start (normal)'
        Write-Host '       start with POOLS_LOG_LEVEL=info + POOLS_DEBUG=0 (normal logging)'
        Write-Host '  3) Stop'
        Write-Host '       stop the verified local process (SQLite keeps committed data)'
        Write-Host '  4) Restart'
        Write-Host '       stop then start again in the current mode'
        Write-Host '  5) Status'
        Write-Host '       PID, port, uptime and the /healthz JSON body'
        Write-Host '  6) Live log tail'
        Write-Host '       follow the newest logs\pools-core-*.jsonl (Ctrl+C returns to the menu)'
        Write-Host '  7) Open Monitor page'
        Write-Host '       http://127.0.0.1:8091/pools/monitor.html in the default browser'
        Write-Host '  8) Open Admin page'
        Write-Host '       http://127.0.0.1:8091/pools/monitor.html in the default browser'
        Write-Host '  9) Store backend'
        Write-Host '       sqlite | json | csv | postgres | redis -> POOLS_STORE in data\launcher.env.json'
        Write-Host ' 10) Show config'
        Write-Host '       effective configuration, launcher env file and store catalog'
        Write-Host ' 11) Diagnostics'
        Write-Host '       toolchain, binary, pid, port, health and log locations'
        Write-Host ' 12) Test (build+test)'
        Write-Host '       go build ./... + go vet ./... + go test ./...'
        Write-Host '  Q) Quit'
        Write-Host '       leave the launcher (any service you started stays up)'
        Write-Host ''
        $choice = Read-Host 'Select'
        if ($choice -in @('q', 'Q')) {
            Write-LauncherLog -Action 'quit' -Result 'ok' -Ms 0
            Write-Host 'Bye.' -ForegroundColor DarkGray
            break
        }
        $map = @{
            '1' = 'start-debug'; '2' = 'start-normal'; '3' = 'stop'; '4' = 'restart'
            '5' = 'status'; '6' = 'tail'; '7' = 'monitor'; '8' = 'admin'
            '9' = 'store'; '10' = 'config'; '11' = 'diagnostics'; '12' = 'test'
        }
        if ($map.ContainsKey($choice)) {
            try { Invoke-CoreAction $map[$choice] } catch { Write-Host $_.Exception.Message -ForegroundColor Red }
        } elseif (![string]::IsNullOrWhiteSpace($choice)) {
            Write-Host "Unknown selection '$choice'. Press 1-12 or Q." -ForegroundColor Red
        }
    }
}

# ------------------------------------------------------------------ main ----
Ensure-Layout
if ($Action -eq 'menu') {
    Show-Menu
    exit 0
}
try {
    Invoke-CoreAction $Action
    exit 0
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
    exit 1
}
