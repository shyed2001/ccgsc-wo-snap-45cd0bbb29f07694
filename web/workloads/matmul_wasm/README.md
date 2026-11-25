# matmul_wasm — the D-W10-10 generic WASM ABI example

A workload-layer example, not platform code. It exists to prove that ANY language
that targets wasm32 can be a CCGSC kernel through ONE byte contract, with no
per-problem adapter anywhere in the platform.

| Item | Value |
|---|---|
| id / version | `matmul` / `1` |
| kind | `wasm-abi` |
| entry | `matmul_scalar.wasm` (892 B) |
| SIMD twin | `matmul_simd.wasm` (1083 B) |
| ABI | [`docs/ccgsc_abi.h`](../../../docs/ccgsc_abi.h), [`docs/WASM_ABI.md`](../../../docs/WASM_ABI.md) |
| source | `matmul.c` |
| build | `build.ps1` (`-Verify` re-builds and compares to `manifest.json`) |

## The contract

```text
unsigned ccgsc_alloc(unsigned len);                 // input region
int      ccgsc_run(unsigned inPtr, unsigned inLen); // output length, or < 0
unsigned ccgsc_out(void);                           // output region
```

The platform writes the task input bytes at `ccgsc_alloc(inLen)`, calls
`ccgsc_run(inPtr, inLen)`, reads the returned length from `ccgsc_out()` and
hashes those bytes in the runner shell. The kernel hashes nothing.

## Input and output

```text
input   : little-endian u32 n, then n*n f32 A, then n*n f32 B
output  : n*n f32 C, row-major, C[i*n+j] = sum_p A[i*n+p] * B[p*n+j]
n       : 160 or 240 (the published ladder)
```

The task input reaches the module as `{"bytesBase64": "..."}` inside the signed
input JSON, so the binding gate covers the exact bytes the kernel reads.

## Build

```powershell
pwsh ./build.ps1            # both variants, measured hashes printed
pwsh ./build.ps1 -Verify    # rebuild elsewhere and compare to manifest.json
```

Scalar and SIMD are separate artifacts and separate identities. The scalar build
never sees `-msimd128`; the SIMD build adds `-msimd128 -DMATMUL_WASM_SIMD=1`,
which selects the 4-wide path. There is no fallback between them.

## Reference per kernel

D-W10-10 keys a reference by **workload id + version + variant**, so
`matmul@1:scalar` and `matmul@1:simd` are two kernels and not one identity with
two answers.

| kernel | 160^3 | 240^3 |
|---|---|---|
| `matmul@1:scalar` | `1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2` | `049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c` |
| `matmul@1:simd` | `1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2` | `049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c` |

Both variants agree with each other and with the JavaScript `matmul` kernel at
both sizes. That agreement is **measured and recorded**, not forced: it holds
because both accumulate in the same fixed `i → j → p` order with a rounded
product (`-ffp-contract=off`, no FMA) over exactly representable operands.

## Tests

```text
node --test tests/matmul_wasm.test.mjs
```

The suite reads `manifest.json`, so a rebuilt artifact whose digest moved fails
the test until the manifest is updated deliberately.
