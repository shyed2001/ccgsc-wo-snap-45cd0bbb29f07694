# CCGSC P2P/Dew — Owner Design Decisions (Proposed, Not Implemented)

This note preserves the isolated P2P/Dew review questions for owner approval. No default, route, authentication or peer transport changes are authorized merely by this document.

# Owner decisions - P2P + Dew candidate, 2026-11-24

These are unresolved implementation/acceptance contracts, not permission requests to repeat approved R14 pure-function work. No deployment or default change is proposed for this run. The same-origin WSS direction, direct same-device/LAN tiers and optional TURN are already stated in WO_R14_P2P_JS_glm.md and need not be reselected.

1. **Default/schema conflict.** Current `schema.js:336,443` says on; API `api.go:870` says disabled; R14 says default off pending staged qualification. For a later change, approve (A) aligning the UI and effective configuration to off until recorded qualification, or (B) keeping the current schema as an explicitly labeled future state? A is recommended to remove misleading enablement. This candidate preserves all defaults and defers the R14 schema/tests, including its unimplemented monitor rows.

2. **Exact signaling and authorization contract.** Which canonical owner design specifies the same-origin WSS route, worker/session binding, allowed signaling messages and per-artifact peer authorization? Please point to the approved contract, or approve a concrete design review before implementation. Existing worker bearer authentication and a guessed peer scope must not silently become permission to enumerate or obtain another artifact. No route, token grant or peer transport was invented here.

3. **Dedicated cache and peer ingest lifecycle.** Approve a separate bounded provenance store and server-only ingest wiring (recommended), or identify an already approved store adapter. The current T104 store holds raw buffers; R14 requires objects. Decide total storage/entry limits and eviction/lifetime, not just the approved 32 MiB item cap. `putVerified` may only receive module artifacts from the trusted server path: a matching digest is not proof of origin. Peer bytes must not be relabeled server-origin or re-served. Results, inputs, receipts and user data remain outside P2P scope.

4. **MIME contract.** The API currently sends application/octet-stream for every artifact (`api.go:104`), while the runner accepts signed generic JS/WASM module kinds. Approve (A) accepting octet-stream under the existing signed digest/kind contract, with explicit rejection of other unexpected response types, or (B) a coordinated server/client typed-MIME contract? A minimizes API change; either requires explicit acceptance criteria. No MIME-specific gate or workload parser was added.

5. **Dew recovery scope.** Is MVP acceptance limited to a still-open tab reconnecting and submitting its retained signed lease/result, or must it survive reload/crash? The server has bounded trusted receipts, but the contributor's pending result is in memory (`contribute.js:683,2788`). Durable recovery would need a separately reviewed bounded outbox, multi-result handling and retention policy. No user storage/data changes were made. Results must still go only to the server.

6. **Staged acceptance before enablement.** Confirm the required two/three-browser/device matrix, peer-hit/bytes-saved thresholds and observation window, both-pool cases, direct and optional TURN evidence, corrupt/oversize/timeout/peer-departure fallback cases, head/server failure recovery, and Dew expiry/re-lease/replay/rotation cases. The supplied WS3 plan requires staged peer-hit/fallback/head-failure evidence but does not provide numeric thresholds here. Local pure-function tests cannot close those gates. Approve a later staging work order only after the runtime contract is settled; no staging or live endpoint work was done in this run.

Delivery limitation: the requested p2p_dew parent directory is not writable in this session. These artifacts are complete under pools-core/finishdash-evidence; copying them to the requested parent requires an environment with that output directory permitted. No sandbox bypass was attempted.
