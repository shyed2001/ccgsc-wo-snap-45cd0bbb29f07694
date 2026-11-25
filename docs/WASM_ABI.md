# The generic CCGSC WASM ABI (D-W10-10)

One byte contract for every workload compiled to wasm32. The platform has no
per-language adapter and no per-problem adapter: a workload is a module that
exports its memory and three functions, takes bytes in and gives bytes out, and
the runner hashes exactly the bytes it reads back.

Normative header: [`ccgsc_abi.h`](./ccgsc_abi.h).

## 1. The contract

| Export | Signature | Meaning |
|---|---|---|
| `memory` | `WebAssembly.Memory` | the module's own linear memory; required |
| `ccgsc_alloc` | `unsigned (unsigned len)` | offset of at least `len` writable bytes for the input |
| `ccgsc_run` | `int (unsigned inPtr, unsigned inLen)` | run one task; **return** = output length, negative = error code |
| `ccgsc_out` | `unsigned (void)` | offset of the first output byte |

The platform, in order:

1. verifies the envelope and the sha256 of every delivered file;
2. classifies the module **before** instantiating it: `memory` and the three
   functions must be exported and the module must import nothing (only
   `env.abort` is tolerated);
3. instantiates with that one stub, calls `ccgsc_alloc(inLen)`, writes the input
   bytes there;
4. calls `ccgsc_run(inPtr, inLen)`; a negative return fails the task with the
   module's own code;
5. reads `outLen` bytes from `ccgsc_out()` and hashes them **in the shell** - a
   digest is never computed inside the realm that ran untrusted code;
6. enforces the memory cap (default 4096 pages = 256 MiB) before and after the
   run, and the output cap (64 MiB).

Pointers are wasm32 linear-memory offsets. A pointer outside
`[0, memory.byteLength)` fails the task instead of trapping the worker.

## 2. A 20-line C example

```c
/* echo.wasm - the smallest correct module: returns the input unchanged. */
#define CAP 65536u
static unsigned char in[CAP], out[CAP];

__attribute__((export_name("ccgsc_alloc")))
unsigned ccgsc_alloc(unsigned len) { return len <= CAP ? (unsigned)&in[0] : 0u; }

__attribute__((export_name("ccgsc_out")))
unsigned ccgsc_out(void) { return (unsigned)&out[0]; }

__attribute__((export_name("ccgsc_run")))
int ccgsc_run(unsigned inPtr, unsigned inLen) {
    const unsigned char *src = (const unsigned char *)inPtr;
    for (unsigned i = 0; i < inLen; i++) out[i] = src[i];
    return (int)inLen;
}
```

## 3. Build lines

The linked module must export exactly the ABI and import nothing.
`--no-entry` is required: there is no start function, the platform calls
`ccgsc_run` itself.

**C / C++ (clang 22, wasm32 target)**

```text
clang --target=wasm32 -nostdlib -O2 -ffp-contract=off -Wl,--no-entry \
  -Wl,--export=ccgsc_alloc -Wl,--export=ccgsc_run -Wl,--export=ccgsc_out \
  -Wl,--max-memory=268435456 echo.c -o echo.wasm
```

`-ffp-contract=off` matters: it forbids FMA contraction, so a float kernel's
bytes are a function of the source arithmetic order and not of the optimizer's
convenience. Add `-msimd128` for the SIMD variant, and build it as a SEPARATE
artifact - a SIMD build is never a fallback for a scalar one.

**C++ name mangling:** `extern "C"` (or `export_name`) is required, otherwise
the export is `_Z12ccgsc_allocj` and the classifier refuses the module.

**Rust**

```rust
#[no_mangle]
pub extern "C" fn ccgsc_alloc(len: u32) -> u32 { /* offset */ }

#[no_mangle]
pub extern "C" fn ccgsc_run(in_ptr: u32, in_len: u32) -> i32 { /* bytes */ }

#[no_mangle]
pub extern "C" fn ccgsc_out() -> u32 { /* offset */ }
```

```text
rustc --target wasm32-unknown-unknown -O --crate-type cdylib echo.rs -o echo.wasm
```

`wasm32-unknown-unknown` imports nothing by default. `wasm32-wasip1` imports
WASI host functions and is therefore refused by the classifier - that is the
rule, not an oversight.

**Zig**

```zig
export fn ccgsc_alloc(len: u32) u32 { return @intFromPtr(&in_buffer); }
export fn ccgsc_run(in_ptr: u32, in_len: u32) i32 { return @intCast(in_len); }
export fn ccgsc_out() u32 { return @intFromPtr(&out_buffer); }
```

```text
zig build-lib echo.zig -target wasm32-freestanding -dynamic -O ReleaseSafe \
  -fno-entry -rdynamic -femit-bin=echo.wasm
```

**TinyGo**

```go
//go:wasmexport ccgsc_alloc
func ccgscAlloc(len uint32) uint32 { return bufferPtr }

//go:wasmexport ccgsc_run
func ccgscRun(inPtr, inLen uint32) int32 { return int32(inLen) }

//go:wasmexport ccgsc_out
func ccgscOut() uint32 { return outPtr }
```

```text
tinygo build -target=wasip1 -march=wasm32 -o echo.wasm ./echo.go
```

TinyGo's `wasip1` target imports WASI functions; build with
`-scheduler=none -no-debug` and check `WebAssembly.Module.imports` before
shipping. AssemblyScript reaches the same ABI through
`--exportRuntime --use abort=` and `@export` functions of type `u32`/`i32`.

## 4. What the platform enforces

| Bound | Default | Where |
|---|---|---|
| artifact bytes | 32 MiB per workload (`MAX_WASM_BYTES`) | before hashing/compiling |
| module memory | 4096 pages = 256 MiB (`wasmMaxMemoryPages`) | on the REAL memory, before and after `ccgsc_run` |
| output bytes | 64 MiB (`MAX_OUTPUT_BYTES`) | after `ccgsc_run` returns |
| task wall time | the lease's `timeoutMs`, 30 s fallback | the shell terminates the worker |
| imports | none; `env.abort` only | classifier, before instantiation |

The module memory bound is checked on `memory.buffer.byteLength` rather than on
the declared maximum, because the JavaScript API does not expose a module's
maximum: a module that allocates above the cap is refused at the run
(`wasm_abi_memory_cap`), before any input is copied in.

A module that fails classification is **declined**, not failed: it is a
malformed workload, and the runner reports `task_declined: wasm_abi_*`.

## 5. Input and output

The task input reaches the module as raw bytes. In the signed input JSON it is
`{"bytesBase64": "..."}`; the runner decodes it only after the input binding
check, so the bytes the module reads are exactly the bytes the envelope
authorizes. The output is whatever `ccgsc_run` reports, and its SHA-256 is the
task's `output.sha256`.

A wasm-abi workload carries no companion files: the byte ABI has no channel for
them, and a job that declares any is refused
(`wasm_abi_files_not_supported`) rather than silently ignored.

## 6. Reference per kernel

The reference hash of a wasm-abi workload is keyed by **workload id + version +
kernel variant** (`matmul@1:scalar`, `matmul@1:simd`). Two variants of one
kernel are two identities, never one identity with two hashes. The matmul
example records both in its `manifest.json`.

Equality with the JavaScript kernel is **measured and recorded, never forced**:
the scalar variant happens to reproduce the JS reference bytes at 160^3 and
240^3 because both accumulate in the same fixed order with the same rounding,
and that agreement is written down as a fact rather than asserted as a rule.
