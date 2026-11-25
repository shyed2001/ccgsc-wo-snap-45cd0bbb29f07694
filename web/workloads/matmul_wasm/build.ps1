<#
================================================================================
 build.ps1  --  matmul_wasm, the D-W10-10 generic WASM ABI example workload
--------------------------------------------------------------------------------
 Compiles matmul.c into two authorized artifacts and records their SHA-256:

     matmul_scalar.wasm     --target=wasm32 -nostdlib -O2 -ffp-contract=off
     matmul_simd.wasm       the same source with -msimd128

 The scalar build never sees -msimd128, so the scalar artifact cannot contain a
 SIMD instruction by accident. Neither build is a fallback for the other: a
 -Simd run that fails FAILS, it does not quietly leave a scalar module under the
 SIMD name.

 The ABI is docs/ccgsc_abi.h: the module exports memory, ccgsc_alloc(len),
 ccgsc_run(inPtr,inLen) -> outLen and ccgsc_out(). --no-entry, because the
 platform calls functions and there is no start function.

 Nothing is installed, no PATH is modified and no fallback toolchain is searched
 for. A missing clang stops the build.

 Usage:
     pwsh ./build.ps1              build both variants
     pwsh ./build.ps1 -Verify      rebuild in a temp folder and compare to the manifest
================================================================================
#>

[CmdletBinding()]
param(
    [string]$Clang = "C:\Program Files\LLVM\bin\clang.exe",
    [switch]$Verify
)

$ErrorActionPreference = 'Stop'
$SourceDir = $PSScriptRoot

function Fail([string]$Message) {
    Write-Host "BUILD.STATUS: FAIL"
    Write-Host "BUILD.REASON: $Message"
    exit 1
}

# SHA-256 without Get-FileHash: that cmdlet is absent from some hosts, and a
# build script that cannot record its own artifact hashes is not a build gate.
function Get-Sha256Hex([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        return (($sha.ComputeHash($stream) | ForEach-Object { $_.ToString('x2') }) -join '')
    } finally {
        $stream.Close()
        $sha.Dispose()
    }
}

# Read the `target_features` custom section structurally: [count: u32][
# prefix byte ('+' enable / '-' disable)][name length: uleb][name]. A string
# search is not evidence here - the section is binary and every name in it is
# length-prefixed.
function Get-TargetFeatures([byte[]]$Bytes) {
    $pos = 8
    $features = @()
    while ($pos -lt $Bytes.Length) {
        $sectionId = $Bytes[$pos]; $pos++
        $size = 0; $shift = 0
        do { $b = $Bytes[$pos]; $pos++; $size = $size -bor (($b -band 0x7F) -shl $shift); $shift += 7 } while (($b -band 0x80) -ne 0)
        $end = $pos + $size
        if ($sectionId -eq 0) {
            $nameLen = 0; $shift = 0
            do { $b = $Bytes[$pos]; $pos++; $nameLen = $nameLen -bor (($b -band 0x7F) -shl $shift); $shift += 7 } while (($b -band 0x80) -ne 0)
            $name = [System.Text.Encoding]::ASCII.GetString($Bytes, $pos, $nameLen)
            $pos += $nameLen
            if ($name -eq 'target_features') {
                # [count: uleb][('+'|'-') nameLen: uleb name]...
                $count = 0; $shift = 0
                do { $b = $Bytes[$pos]; $pos++; $count = $count -bor (($b -band 0x7F) -shl $shift); $shift += 7 } while (($b -band 0x80) -ne 0)
                for ($i = 0; $i -lt $count; $i++) {
                    $prefix = [char]$Bytes[$pos]; $pos++
                    $len = 0; $shift = 0
                    do { $b = $Bytes[$pos]; $pos++; $len = $len -bor (($b -band 0x7F) -shl $shift); $shift += 7 } while (($b -band 0x80) -ne 0)
                    $feat = [System.Text.Encoding]::ASCII.GetString($Bytes, $pos, $len)
                    $pos += $len
                    $features += ($prefix + $feat)
                }
            }
        }
        $pos = $end
    }
    return $features
}

if (-not (Test-Path -LiteralPath $Clang)) { Fail "clang not found: $Clang" }
$Source = Join-Path $SourceDir 'matmul.c'
if (-not (Test-Path -LiteralPath $Source)) { Fail "source not found: $Source" }

$CommonArgs = @(
    '--target=wasm32'
    '-nostdlib'
    '-O2'
    '-ffp-contract=off'
    '-Wall'
    '-Wextra'
    '-Wpedantic'
    '-Wconversion'
    '-Wshadow'
    '-Wl,--no-entry'
    '-Wl,--export=ccgsc_alloc'
    '-Wl,--export=ccgsc_run'
    '-Wl,--export=ccgsc_out'
    '-Wl,--max-memory=268435456'
)

$Variants = @(
    @{ Name = 'matmul_scalar'; Simd = $false },
    @{ Name = 'matmul_simd';   Simd = $true  }
)

$OutDir = if ($Verify) { Join-Path ([System.IO.Path]::GetTempPath()) ('matmul_wasm_verify_' + [guid]::NewGuid().ToString('N')) } else { $SourceDir }
if ($Verify) { New-Item -ItemType Directory -Path $OutDir | Out-Null }

try {
    foreach ($variant in $Variants) {
        $out = Join-Path $OutDir ($variant.Name + '.wasm')
        if (Test-Path -LiteralPath $out) { Remove-Item -LiteralPath $out -Force }
        $clangArgs = $CommonArgs
        if ($variant.Simd) { $clangArgs = $clangArgs + @('-msimd128', '-DMATMUL_WASM_SIMD=1') }
        $clangArgs = $clangArgs + @($Source, '-o', $out)
        Write-Host ("BUILD.COMMAND: `"{0}`" {1}" -f $Clang, ($clangArgs -join ' '))
        $diagnostics = & $Clang @clangArgs 2>&1
        $exit = $LASTEXITCODE
        if ($diagnostics) { $diagnostics | ForEach-Object { Write-Host "  $_" } }
        if ($exit -ne 0) { Fail "$($variant.Name) compile failed (exit $exit)" }
        if (-not (Test-Path -LiteralPath $out)) { Fail "$($variant.Name) produced no artifact" }
        $item = Get-Item -LiteralPath $out
        $hash = Get-Sha256Hex $out
        Write-Host "BUILD.ARTIFACT: $out"
        Write-Host "BUILD.ARTIFACT_BYTES: $($item.Length)"
        Write-Host "BUILD.ARTIFACT_SHA256: $hash"

        # Per-variant measurement, used by the manifest check below (no lookup
        # table: the loop is two entries and a table here only hides the keys).
        $variant.Bytes = [int64]$item.Length
        $variant.Sha256 = $hash
    }

    # The module must carry the ABI and nothing else. Read structurally, not by
    # grepping the binary for strings. SIMD presence is the `target_features`
    # custom section clang emits ("+simd128"): a raw 0xFD opcode scan is NOT
    # evidence, because a small scalar function can vectorize into nothing at all.
    $expectSimd = @{ matmul_scalar = $false; matmul_simd = $true }
    foreach ($variant in $Variants) {
        $bytes = [System.IO.File]::ReadAllBytes((Join-Path $OutDir ($variant.Name + '.wasm')))
        if ($bytes.Length -lt 8 -or $bytes[0] -ne 0x00 -or $bytes[1] -ne 0x61 -or
            $bytes[2] -ne 0x73 -or $bytes[3] -ne 0x6D) {
            Fail "$($variant.Name) is not a WebAssembly module (bad magic)"
        }
        $features = Get-TargetFeatures $bytes
        $declaresSimd = ($features -contains '+simd128')
        Write-Host "BUILD.KERNEL_FEATURES: $($features -join ' ')"
        if ($expectSimd[$variant.Name] -and -not $declaresSimd) {
            Fail "$($variant.Name) does not declare the simd128 target feature"
        }
        if (-not $expectSimd[$variant.Name] -and $declaresSimd) {
            Fail "$($variant.Name) declares the simd128 target feature but is the scalar build"
        }
        Write-Host "BUILD.KERNEL_SCAN: PASS ($($variant.Name))"
    }

    if ($Verify) {
        $manifest = Get-Content -LiteralPath (Join-Path $SourceDir 'manifest.json') -Raw | ConvertFrom-Json
        $mismatch = @()
        foreach ($kernel in $manifest.kernels) {
            $variantName = ([string]($kernel.artifact)).Replace('.wasm', '')
            $built = $Variants | Where-Object { $_.Name -eq $variantName } | Select-Object -First 1
            if ($null -eq $built) {
                $mismatch += "unknown artifact $($kernel.artifact)"
                continue
            }
            if ($kernel.sha256 -ne $built.Sha256) {
                $mismatch += "$($kernel.id) manifest $($kernel.sha256) != measured $($built.Sha256)"
            }
            if ([int64]$kernel.bytes -ne $built.Bytes) {
                $mismatch += "$($kernel.id) manifest $($kernel.bytes) bytes != measured $($built.Bytes)"
            }
        }
        if ($mismatch.Count -gt 0) {
            $mismatch | ForEach-Object { Write-Host "  MISMATCH: $_" }
            Fail "the manifest does not describe the built artifacts"
        }
        Write-Host "BUILD.MANIFEST_VERIFY: PASS ($($manifest.kernels.Count) kernels)"
    }
} finally {
    if ($Verify -and (Test-Path -LiteralPath $OutDir)) {
        Remove-Item -LiteralPath $OutDir -Recurse -Force
    }
}

Write-Host "BUILD.STATUS: PASS"
exit 0
