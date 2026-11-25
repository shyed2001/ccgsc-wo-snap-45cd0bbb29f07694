# WO-SPIKE2 - sandbox spike v2 (tmp only)

Author: hermes MiniMax-M3 for Claude Opus 5.5. Created 2026-11-17 (Dhaka).
Design truth: `F:/tmp/wwpp/draft/CCGSC_TWO_POOLS_DESIGN.md` sections 3, 10, 11, 12
(review fixes WWPP-R39.1). Base harness (read only, not modified):
`F:/tmp/wwpp/spike_sandbox/{parent.html,runner.html,serve.py,cdp_run.mjs}`.

What/why: spike v2 measures the parts of the two-pool design that spike v1 did not cover -
the MessageChannel bootstrap and relay (WWPP-R39.1), the signed task envelope, the worker cap,
Web Locks leader election, the GPU sandbox question and the thesis timing metrics.
Everything lives in this one new folder; nothing outside `F:/tmp/wwpp/spike_sandbox2/` is written.

## Files

| File | Role |
|---|---|
| `parent.html` | Demo-origin parent page. Owns the MessageChannel, signs the Ed25519 envelope, runs the Web Lock check, prints the verdict. |
| `runner.html` | Trusted run-origin iframe (`sandbox="allow-scripts"`, no `allow-same-origin` -> opaque origin). Owns the compute workers, compiles WASM, hashes, verifies the signature, probes WebGPU. |
| `serve.py` | Serves this folder on 8711 (parent) and 8712 (run origin) + `POST /report` sink. |
| `cdp_run.mjs` | Drives `parent.html` in headless Edge over CDP and prints the log. |
| `README.md` | This file: how to run + what each check means. |

## How to run

```
cd F:/tmp/wwpp/spike_sandbox2
python serve.py            # parent http://127.0.0.1:8711/parent.html , run origin 8712
# in a second shell
node cdp_run.mjs            # headless Edge, prints the log and the SPIKE2 verdict
```

Manual / other browsers: open `http://127.0.0.1:8711/parent.html` in Edge, Chrome, Firefox or
Android Chrome. The page writes the log into `#log`, the verdict into `document.title`, and POSTs
the `CHECK` lines to `/report`, which `serve.py` prints to its own stdout - so a phone on the
tailnet can be checked without DevTools (`SPIKE_HOST=<tailnet IP> python serve.py`).

Options: `BROWSER=<path>` (browser binary), `SPIKE_PORT=8711` (parent port),
`SPIKE_GPU=1` (keep the GPU stack on with SwiftShader instead of `--disable-gpu`),
`CDP_PORT=9334` (CDP debug port).

## The checks

Each check prints exactly one line `CHECK <name> PASS|FAIL|SKIP <detail>` in `#log` and in the
`/report` payload.

1. `port_handshake` - The parent creates one `MessageChannel` and transfers `port2` to the known
   iframe with a single `window.postMessage({type:'init'}, '*', [port2])`. After that the parent
   ignores **every** window message and talks only over the port. The runner accepts that one
   bootstrap only from `event.source === parent` and the exact demo origin, and acks with a fresh
   nonce over the port. Two negative controls:
   - a second, sandboxed `srcdoc` iframe forges a **poisoned** `{type:'result', add:999}` via
     `window.postMessage`; the parent must ignore it (`forged_applied=0`, `final_add=5`);
   - the parent sends a **second** bootstrap right after the first; the runner must refuse it
     (`runner_bootstrap_rejected=1 reject_reasons={"already_bootstrapped":1}`).
   The verdict variable `finalAdd` is installed as a trapped global (write counted and attributed),
   so a window result that was wrongly applied is detected even if a later legitimate write would
   overwrite it - ordering cannot hide the bug. PASS requires `verdict_vrites=1` and
   `verdict_violations=0`. Guards the R39.1 rule "never authorize a sender because
   `event.origin === 'null'`".
2. `compile_bytes` - The runner calls `WebAssembly.compile()` on the **transferred** ArrayBuffer
   (no fetch, no `compileStreaming`), instantiates it and checks `add(2,3)=5` and `sum(1000)=499500`.
   The WASM bytes are the same 87 bytes as spike v1, so the two spikes stay comparable.
3. `sha256_in_iframe` - Inside the opaque iframe `crypto.subtle.digest('SHA-256', bytes)` is
   compared to the hash the parent sent, and a 1-byte-tampered copy must produce a different hash
   (rejected). SKIP with a reason if `crypto.subtle` is undefined in the opaque origin.
   Measured on headless Edge 2026-11-17: `crypto.subtle` **is** available in the opaque origin,
   so this check runs rather than skipping.
4. `ed25519_envelope` - The parent (playing the server) generates an Ed25519 key, signs the
   canonical UTF-8 JSON envelope `{jobId,taskId,artifactSha256,inputSha256,leaseExp}` and hands the
   runner the raw public key + signature. The runner verifies **before** running any task, re-checks
   that the canonical JSON matches the signed bytes, and proves a tampered envelope (one flipped hex
   digit in `artifactSha256`) fails verification. If the signature does not verify, the compute is
   skipped (`executed=false`) - the fail-closed behaviour of PDL step 1. SKIP with a reason if
   Ed25519 is unsupported.
5. `webgpu` - The iframe tag carries `allow="webgpu"`. The runner reports `navigator.gpu`
   presence, whether `requestAdapter()` returned an adapter, and the same probe **inside a compute
   worker**. PASS when an adapter is available, SKIP (never FAIL) when there is no GPU - the
   §11 rule is "advertise `webgpu=false`", not "fail the spike".
6. `worker_cap` - Only the trusted runner creates compute workers. `cap = min(4, hardwareConcurrency-1)`.
   The job asks for **16** workers; the runner must refuse and start exactly `cap`. This is the
   R39.1 "runner cap" gate: the count is enforced where the workers are created, not by the requester.
7. `web_lock` - The parent takes `navigator.locks.request('ccgsc-leader', {ifAvailable:true})` and
   reports `leader=1|0`. Leader election only - per R39.1, locks do not keep a tab alive.
8. `timings` - Thesis metrics (WWPP-R37.2): `compile_ms` (WASM compile), `sha256_ms_per_MB`,
   `ed25519_sign_ms`, `ed25519_verify_ms`, `port_rt_us_p50`/`p95` (parent <-> runner MessagePort
   round trip) and `worker_rt_us_p50`/`p95` (runner <-> compute worker round trip). Both round-trip
   series use 10 warmup + 50 measured samples and report p50/p95, not only a mean (§16 asks for
   warmup and percentiles). Spike v1 measured 54-80 us desktop and 436-584 us on Android; these
   numbers are the v2 baseline for the same paths.

## Measured on headless Edge, 2026-11-17 (12 logical cores)

```
CHECK port_handshake PASS nonce_ok=1 forged_result_seen=1 forged_add=999 forged_applied=0 final_add=5
                       port_results=1 window_results=1 window_msgs_ignored=1 pre_init_msgs=0
                       runner_bootstrap_rejected=1 reject_reasons={"already_bootstrapped":1}
                       verdict_writes=1 verdict_violations=0 channel=MessageChannel
CHECK compile_bytes PASS bytes=87 compile_ms=0.70 add_2_3=5 sum_1000=499500 src=ArrayBuffer fetch_used=0
CHECK sha256_in_iframe PASS match=1 tampered_rejected=1 expected=9cf11287b9124280 ... tampered=2c465407dac7b304
CHECK ed25519_envelope PASS verify_ok=1 canonical_match=1 tampered_rejected=1 fields=5 verify_ms=0.40
CHECK worker_cap PASS requested=16 cap=4 started=4 refused=12 hw=12 gate=refused
CHECK webgpu SKIP gpu_present=1 adapter=0 worker_gpu_present=0 worker_adapter=0 allow_attr=webgpu reason=no_adapter
CHECK web_lock PASS leader=1 name=ccgsc-leader if_available=1 callback=1
CHECK timings PASS compile_ms=0.70 sha256_ms_per_MB=1.08 ed25519_sign_ms=0.40 ed25519_verify_ms=0.40
                   port_rt_us_p50=100.00 port_rt_us_p95=400.00 worker_rt_us_p50=100.00 worker_rt_us_p95=100.00
SPIKE2 PASS
```

Notes on the two skips: `webgpu` is SKIP because `navigator.gpu` exists but `requestAdapter()`
returns null in headless (no adapter on this machine) - per §11 the correct answer is
"advertise `webgpu=false`", not "fail the spike". `crypto.subtle` **is** available in the opaque
origin on this browser, so `sha256_in_iframe` and `ed25519_envelope` actually run instead of
skipping - this was measured before writing the checks.

## Negative controls (are the checks falsifiable?)

A check that can only pass proves nothing. Each check was mutated on a **copy** in a scratch
folder (the delivered files were never mutated) and re-run; the expected FAIL was observed:

| Mutation | Observed |
|---|---|
| Parent applies the forged window result | `port_handshake FAIL verdict_writes=3 verdict_violations=2` + 2 `VIOLATION` lines -> `SPIKE2 FAIL` |
| Runner worker-cap gate removed | `worker_cap FAIL requested=16 cap=4 started=16 gate=none` -> `SPIKE2 FAIL` |
| Ed25519 negative control broken (tampered envelope accepted) | `ed25519_envelope FAIL tampered_rejected=0` -> `SPIKE2 FAIL` |
| The 1-byte sha256 tamper removed | `sha256_in_iframe FAIL tampered_rejected=0` -> `SPIKE2 FAIL` |

The unmutated files still print `SPIKE2 PASS` with `verdict_writes=1 verdict_violations=0`, so the
trap does not false-positive.

## Verdict

`SPIKE2 PASS` when `port_handshake`, `compile_bytes` and `worker_cap` are PASS and
`sha256_in_iframe` / `ed25519_envelope` are PASS-or-SKIP. Otherwise `SPIKE2 FAIL`.
The same string is written to `document.title`.

## Guardrails honoured

- New folder only (`F:/tmp/wwpp/spike_sandbox2/`); the base spike folder is read-only here.
- `serve.py` binds `127.0.0.1` unless `SPIKE_HOST` is set.
- Every file carries a header (path, author, date, what/why) and an `END OF FILE` footer.
- Each check block is commented with the design rule it exercises.

## Not covered (next spikes)

Freeze/resume, leader death, reconnect, nested-worker cap bypass from developer code, WGSL
transfer/dispatch/readback, device loss and the CPU fallback path (§16 `sandbox` and
`gpu-sandbox` rows).
