# matmul workload (W10 reference workload)

This folder is WORKLOAD-owned (owner hard rule CR-53): the platform never imports
or inlines anything from here. Contents:

- `matmul_kernel.js` - the kernel, ABI `run(input, ctx) -> { output, meta }`
  (design section 4). Imports nothing, fetches nothing, hashes nothing.
- `manifest.json` - build aid only: id, version, kind, entry, the entry sha256
  pin, runtime, input/output schema and the published reference hashes. It is
  never trusted at runtime; trust comes from the server-signed task envelope
  (D-W10-3). The server seed checks `sha256(entry) == manifest.sha256`.
- `tests/` - the kernel tests (moved here per WO_SPLIT_W10_RESUME step 1; they
  may import `platform/hash.js`, test only).

## C/C++ WASM adapter slot (D-W10-1, reserved - do NOT build now)

A future C/C++ kernel ships as a workload-owned JS adapter IN THIS FOLDER: an
ES module exporting `async function run(input, ctx)` that instantiates the
verified `.wasm` bytes, marshals typed arrays in/out of linear memory, cleans up,
and returns `{ output, meta }` in the same ABI (CPU/GPU fallback included). The
platform stays generic: the runner imports the adapter module exactly like it
imports `matmul_kernel.js` today and never branches on a kernel kind. The
existing C++ source lives in `CCGS_WASM/AI_C_CPP/Matrix_CPP_WASM` and is not
built in W10.
