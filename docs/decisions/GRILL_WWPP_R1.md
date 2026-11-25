# WW-PP grill round 1 - owner answers (verbatim intent, compressed)

File: F:\tmp\wwpp\GRILL_WWPP_R1.md. Author: Claude Opus 5.5 for Shyed Shahriar. Created 2026-11-11 (~05:30Z).
Purpose: keep the owner's WW-PP (Web Worker Process Pool / Parallel Processing / Worker Pool) decisions until they are
appended to the plan home [[CCGSC_HYBRID_NATIVE_MP_PP_ARCHITECTURE_V1]] after an MCQ with the diff shown (rule A81a).
Tracking: CR-43 MPC-09 (web/WASM twin) + new WW-PP item (ID assigned when written into CR-43).

## Owner intent (2026-11-11)
A generic web browser worker pool on the VPS, better than a thread pool or process pool. Developer users send work:
code (C/C++/Go/Rust compiled to WASM + glue JS) and data. Browser Web Workers take tasks, use browser memory and
IndexedDB if needed, compute, and hand results back through an out-queue the developer defines. The native C/C++/Go/
Rust side enqueues WASM + glue JS; server code is separate. "Plan first from what is in my plan, not random runs."

## Decisions
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R1.1 | Reuse existing pool or new | New generic MP/PP module with its own routes (/mppp/v1/...), reusing pool_gateway membership, worker page shell, Redis liveness, oracle idea; old Matrix/SSPrime/IsPrime lanes unchanged, later registered workloads. **In Node and Go.** Fully separate service (Rust or C++) maybe later, not now. |
| WWPP-R1.2 | What code may developers send | "All any" - any code, with review by CCGSC Owner, Admin and Directors before it runs. |
| WWPP-R1.3 | Server language | Node now (inside existing server) plus Go (CCGSC_TP skeleton); C++ or Rust later. |
| WWPP-R1.4 | First milestone | Design doc + API spec first (frozen, reviewed by codex/agy), then skeleton end-to-end, with all 3 lanes (Matrix, SSPrime, IsPrime). |
| WWPP-R1.5 | Director page | The TP page being built becomes the new WW-PP director page, with options to select Node or Go back-end lanes, "as we planned before". (Inventory WO WWPP-INV-01 checks where that plan and page are.) |
| WWPP-R1.6 | Inventory | Cheap lane reads previous plans, current plan and what current code can do (cmdc deepseek-v4-pro, WO F:\tmp\wwpp\WO_WWPP_INVENTORY_01.md, output cmdc_inventory.json). |

## Owner question to answer: "any problem?" (native enqueues WASM + glue JS to web workers)
Short answer for the owner (to confirm in round 2): no blocker in principle. Points the design must cover:
1. Glue JS from developers can call fetch/network and DOM-less APIs inside the worker: run only reviewed glue, or use
   one generic host glue so only the WASM is per-job (safer, simpler review).
2. WASM is sandboxed but can still burn CPU/RAM on volunteer devices: time, memory and size limits per task.
3. Results from browsers are untrusted: verification (hash, server re-check, or redundant k-of-n) before acceptance.
4. Private data: G16 privacy levels (public-only default, owner-nodes-only, any-worker with warning).
5. Go-compiled WASM is large (TinyGo smaller); Rust/C/C++ via emscripten/wasm-bindgen fit best.

## Round 2 answers (2026-11-11 ~06:00Z)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R2.1 | Go lane placement | Go as a separate service on localhost, nginx routes `/mppp-go/` to it (standard way). "Go for it now." Owner asked which option reuses code, is future-proof, eases migration to compiled languages, suits VPS + local demos (answered in chat: sidecar wins all four; reuses CCGSC_TP which is already a standalone server on 127.0.0.1:8090). |
| WWPP-R2.2 | Results out | Keep all options now, finalize later: poll + download, live stream (WebSocket/SSE), webhook callback, result queue per job. |
| WWPP-R2.3 | Review flow | Keep all options now (submit-review-pin, two-person rule, trusted auto-approve, owner only). |
| WWPP-R2.4 | Worker glue | Both, per workload: generic host glue by default; custom glue allowed per workload with stricter review. |

Inventory (cmdc WWPP-INV-01, `F:\tmp\wwpp\cmdc_inventory.md`): no TP director page in code (planned `public/tp/director.html`,
-2nd Extension.md:281); Go `CCGSC_TP` = health + capabilities only; frozen v16 already specifies the generic job API,
registry, leases, fencing, verification, results and PG DDL; reusable now: pool_gateway.js, matrix.js lease/report/settle,
matrix_oracle.js, prime.js pull, matrix_client_executor_pool.mjs (needs TP-A01 message fix), worker shell, IndexedDB
cache, auth roles. Contradictions C1-C8 listed there (C7: "Node or Go" vs frozen `go|rust` engine enum).

## Round 3 answers (2026-11-11 ~06:15Z)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R3.1 | "Node or Go" (C7) | Engine list `node|go|rust` fed by GET /capabilities: node = Node module /mppp/v1, go = Go service /mppp-go/v1, rust/cpp later shown disabled until qualified. |
| WWPP-R3.2 | Contract | New WW-PP contract v1 written fresh from the WW-PP answers, reusing v16's proven shapes (job, task, lease, report, fencing, verification, results) under /mppp/v1 and /mppp-go/v1, plus reuse of existing components. v16 frozen, reference only. **"TP MT is out of the design now."** |
| WWPP-R3.3 | Truth store | Server PostgreSQL authoritative (jobs, tasks, leases, one accepted result per task by unique constraint); Redis only membership/liveness. Native side: SQLite + JSON (as agreed), browser workers: IndexedDB; neither is an authority for remote results. |
| WWPP-R3.4 | Split + verify | Keep all options, selectable on the Director and accepted from native developer requests by API rules: developer split, server split helper, hash + sample recompute, redundant k-of-n. "DRY KISS, don't make it complicated. Follow the other Process Pool design; this is our new web worker process pool on the software-defined, crowd-connected hardware WW/WWP MP/PP architecture." |

## Round 4 answers (2026-11-11 ~06:30Z)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R4.1 | Spec home | All three, small: (a) new small file `CCGS_WASM/CCGSC_MPPP/spec/CCGSC_WWPP_WEB_WORKER_PROCESS_POOL_CONTRACT_V1.md`; (b) a new WW-PP section in `CCGSC_DISTRIBUTED_MULTIPROCESSING_PROCESS_POOL_API_V1.md`; (c) a small CCGSC system-design vault file next to the hybrid architecture file. Plus links, refs, pointers, index rows in all related tables, files, folders and sections. |
| WWPP-R4.2 | Format | For humans now: plain prose + tables (short, readable; schemas later). Later for HMI/machines: Gherkin + PDL + JSON schema + Mermaid views, and TLA+/PlusCal for lease fencing. |
| WWPP-R4.3 | Writers | hermes MiniMax-M3 drafts; agy Gemini Argon + cmdc review; codex after reset; Claude verifies against the answers; owner approves via MCQ with diff. |
| WWPP-R4.4 | WASM ABI | Decide in the spec: draft compares shared `ccgsc-compute-1` vs a separate web ABI; choose at review. |
| WWPP-R4.x | Working style | More focused atomic granular MCQ sessions with custom input and pros/cons on every option. JS tests: use `bun test` where it works and is faster; Go/Rust test runners for speed. Use all machines, all AI and Tier 0 tools in parallel. |

## Round 5 answers (2026-11-11 ~07:50Z) - contract open questions (D1 section 13)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R5.1 | Job identity | One shared id space for WW-PP and native MP/PP jobs (supersedes E38.1=B for WW-PP; one id for status and cancel everywhere). |
| WWPP-R5.2 | Default verification | Hash + sample recompute (all modes stay selectable). |
| WWPP-R5.3 | ABI | Shared kernel core ABI `ccgsc-compute-1` + thin web envelope `ccgsc-web-compute-1` (limits, artifact hashes, lease generation). |
| WWPP-R5.4 | Custom glue network | Keep all 3 (no network / host allow-list / allow-list + per-job URL grants) "or better options" in the Admin/Director settings page. First user is the owner only; all allowed in the MSc CSE MVP and PhD demo; secure later for FVP / BVP / CVP (open market). |
Still open from D1 section 13: Q2 engine list growth, Q3 split helper default, Q5 result queue semantics, Q6 review-flow
default, Q8 verification-mode semantics, Q10 owner-node identity, Q11 audit tamper-evidence.

## Round 6 answers (2026-11-11 ~08:00Z)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R6.1 | Engine list | `node`, `go`, `rust`, `cpp` in the enum from day one; rust and cpp shown disabled until qualified. The API must take calls from C/C++, Python, Node and Rust native apps (so: plain HTTPS + JSON, language-neutral; client SDKs later). |
| WWPP-R6.2 | Split default | Developer split is the default. CCGSC server rules + the workload manifest declare a per-workload default (e.g. IsPrime/SSPrime ranges = server split, Matrix = developer split). All available as settings options. Final/generic versions: splitting is the developer's job; server split only in testing / MVP. Owner asked "how should the server split?" (answered in chat: rule declared in the manifest - range split, array/blob chunk split, matrix pair split). |
| WWPP-R6.3 | Result queue | Keep both in Director settings: per-job Redis stream FIFO with ack + expiry (MVP default) and durable per-developer PostgreSQL queue with replay. |
| WWPP-R6.4 | Review default | Submit-review-pin (one reviewer, SHA-256 pin); other flows stay selectable. |
Still open: Q8 verification-mode semantics, Q10 owner-node identity, Q11 audit tamper-evidence.

## Round 7 answers (2026-11-11 ~08:10Z)
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R7.1 | Verification undecided | Default: reject and retry on other workers (max attempts), then FAILED with evidence; hold-for-Director and escalate-to-k-of-n stay selectable. |
| WWPP-R7.2 | Owner-node identity | Keep all options in the Director/Admin pages: signed per-node token, Owner/Admin login on the worker, tailnet/IP allow-list. |
| WWPP-R7.3 | Audit | Keep all options selectable from the Director page: append-only table, per-job hash chain, table + periodic checkpoints. |
All 11 D1 section-13 questions now answered (R5-R7). Pattern across rounds: keep every option as a Director/Admin
setting, set one simple default for the MVP, harden for open-market versions later.

## Round 8 answers (2026-11-11 ~09:00Z) - drafts v3
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R8.1 | Next step for drafts v3 | Review v3 (agy + cmdc, hermes fixes, codex after reset), then place in the 3 homes with pointer rows, ask before commit. |
| WWPP-R8.2 | Audit default | Per-job hash chain, reusing the existing HCIA chain design if possible; keep all options on the Director; add a "no audit chain" option for tests and demos. |
| WWPP-R8.3 | Owner-node default | Signed per-node token; keep the other options. Owner note: any server API credential (developer/scoped token) is also a form of credential to use the pool. |

## Round 9 (2026-11-11 ~09:15Z) - P2P and dew/offline computing
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R9.2 | Dew / offline | Owner definition (verbatim intent): "the worker browser tab or worker or device receives work, task, data and code, it may go out of network or the platform for some time, but it can submit the results later too". Design (PROPOSED wording): offline lease with long deadline + artifacts cached by hash in IndexedDB + local result store + late submit; server accepts if lease generation valid, else first verified result wins and the late one is kept as evidence (fencing, one accepted result per task). Broader dew option (local LAN coordinator serving nearby devices) noted for later. |
| WWPP-R9.1 | P2P scope | NOT ANSWERED. Recommended (PROPOSED): P2P-Lite peer-first for pinned artifacts + input blobs, hash-checked, server fallback; results always to the server. Options kept: task hand-off between peers, result relay via peers, none. |
| WWPP-R9.3 | Plan place | NOT ANSWERED. Recommended (PROPOSED): Director settings now, default off in M1 (server-only skeleton); build in M2 (P2P artifacts + offline finish/late submit), M3 (offline batch leases, local dew pool). |

## Round 9/10 answers (2026-11-11 ~11:40Z) - supersede the PROPOSED R9.1/R9.3 rows above
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R9.1 | P2P scope | All three kept as Director settings (artifacts + input blobs, task hand-off, result relay), artifacts-only default; each with legends + pros/cons shown on the Director panel. Plus peer selection criteria/rules as settings, "as we did before": same computer, same phone, same LAN, very close network proximity, etc. |
| WWPP-R9.3 | When | P2P and dew/offline (take work, go offline, submit later) in the FIRST milestone M1. |
| WWPP-R10.1 | Tab/worker naming | Defer; discuss again in detail later. Owner question: can two or more workers in one tab share memory or code like native threads? (answered in chat: by default no shared memory, separate heaps, message passing; compiled WASM module can be shared/posted so code compiles once; true shared memory only with SharedArrayBuffer / shared WebAssembly.Memory + COOP/COEP cross-origin isolation, excluded in v1 by MP-08 - a candidate later option). |

## Round 10 answers (2026-11-11 ~12:00Z) - worker host design
| ID | Question | Owner answer |
|---|---|---|
| WWPP-R10.0 | Shared memory | NO SharedArrayBuffer / shared WebAssembly.Memory / COOP-COEP in WW-PP, now or later ("problematic and not supported everywhere"). |
| WWPP-R10.2 | Code | Default: tab fetches + SHA-256 checks + compiles the WASM once, posts the compiled WebAssembly.Module to its N worker slots (each instantiates its own memory). Plus a Director setting to compile per worker (fallback/debug). |
| WWPP-R10.3 | Data | Both as settings: transfer task slices zero-copy from the tab to workers (default) and workers reading their slice from IndexedDB/OPFS by hash (for large read-only inputs). Results transferred back. |

| WWPP-R10.4 | Workers per tab | Director setting: default 1 (keeps G19=B); Director may set 1-8 or `auto` = device logical CPUs minus 1 (navigator.hardwareConcurrency), never above the server cap or the device CPUs. Plus an optional per-device-class setting: phones 1, laptops 2, desktops 4, owner nodes auto; all editable. |

| WWPP-R10.1 | Naming (decided ~12:10Z) | Official terms: **Web Process** = one browser tab, also called **worker host (tab)**; **web worker thread** = one Web Worker in that tab, also called **execution slot (worker)** - always "web worker thread", never bare "thread"; **Web Process Pool** = all registered tabs across devices. Note always beside it: web worker threads run on their own OS threads and share the tab's compiled code, but have PRIVATE memory and use message passing / zero-copy transfer (no shared memory, R10.0). Both name pairs used (MP/PP framing + host/slot precision). |

### R10.1 rationale (owner asked to keep this for later use, 2026-11-11)
Owner question: "Can we now call our web processes / web process pool as having worker threads in each process, the
way we are designing them?" Answer: yes, with one qualifier.

| Native thread property | Our web worker thread | Same? |
|---|---|---|
| Runs on its own OS thread, in parallel | Each Web Worker runs on a real OS thread inside the tab's renderer | yes |
| Lives inside one process | Workers belong to one tab (worker host) | yes |
| Shares the process's loaded code | Tab compiles WASM once and posts the module to its workers (R10.2) | yes (compiled code shared, each has its own instance) |
| Started and stopped by the process | Tab creates/terminates its workers; count = workers-per-tab setting (R10.4) | yes |
| Shares the process's memory | No: private memory, message passing / zero-copy transfer (R10.0, R10.3) | no |

Accurate name: "web process with web worker threads (private memory, message passing)". Same model as Node.js
`worker_threads` without SharedArrayBuffer, or actor-style threads. Fits the thesis MP/PP framing (processes = tabs,
threads = workers) as long as the private-memory note sits next to it; this also keeps the 2026-11-10 rule that
browser workers are not OS processes or pthreads.

## Round 11 (2026-11-11 ~13:00Z, after agy + cmdc review of draft V7: F:\tmp\wwpp\review_v7\REVIEW_agy.md, REVIEW_cmdc.md)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R11.1 | Job failure rule | Default: job ends `COMPLETED_WITH_FAILURES`, other tasks keep running, failed task ids listed. Director setting per job: `partial` (default) / `fail-fast` (any failed task fails the job, queued/leased siblings cancelled) / `threshold` (job FAILED when failed tasks exceed N %). Owner picked "Partial + setting" and "Threshold setting". |
| WWPP-R11.2 | Default verification sample rate (refines R5.2) | Per-workload manifest field for the recompute rate; Director may raise it. MVP default: first task of every worker + 5 % random, recomputed on another worker. (matrix_oracle.js default 0 % must not be the WW-PP default.) |
| WWPP-R11.3 | Custom glue sandbox | MVP keeps R5.4 (all glue network policies allowed) for Owner/Admin/Director code only. A real sandbox (worker CSP connect-src header, no RTCPeerConnection, fresh WASM instance per task) is a written gate that must pass before any outside developer code runs. Owner words: "Sandbox in MVP BVP FVP CVP later" = the sandbox lands in the later versions after MVP, not in M1. |

## Round 12 (2026-11-11 ~15:10Z, open points from draft V9 / hermes WO 09)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R12.1 | Review quorum default | MSc MVP (version "MVP 100"): 1-of-2 reviewers must agree. PhD MVP ("MVP 200"): 2-of-2 must both agree. General k-of-n later. |
| WWPP-R12.2 | Audit write path (with R8.2 hash chain, R3.3 PG main) | Default: hash-chained rows in a PostgreSQL audit table + a security log that cannot be turned off; HCIA reused for hashing. Options kept: HCIA file store, or both (PG main + HCIA mirror). Director page selector: both, or any one. |
| WWPP-R12.3 | Proposed limits + error-code names (32 MiB WASM / 8 MiB glue upload, 30000 ms / 256 MiB per task, names in CCGSC_WWPP_ERROR_CODES_V1.md) | Accept, measure first: keep them PROPOSED; set real values after the first M1 test runs. |
| WWPP-R12.4 | Result delivery + peer selection defaults | All options and all combinations selectable by the Director. Defaults: delivery poll + download; peers by close proximity + same LAN. |
| — | R11.3 sandbox timing (hermes Q-A) | Not re-asked: R11.3 already decided (gate before outside developer code, after MVP). |

## Round 13 (2026-11-11 ~17:00Z, owner questions from WEBREVIEW-WWPP-V10, ChatGPT review)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R13.1 | Review flows vs R12.1 quorum | Owner code exempt: the 1-of-2 (MVP 100) / 2-of-2 (MVP 200) floor applies to outside code only; Owner/Admin/Director code may use auto-approve or 1 reviewer in any version. |
| WWPP-R13.2 | Late offline result after re-lease (R9.2 first winner) | "Keep all options in the director page, recommended one is the default." Options: (a) DEFAULT archived lease id + attempt match, job/workload not revoked/terminal, task not ACCEPTED, independent verification, competing lease retired in the same transaction; (b) accept only if no other attempt reported; (c) drop late results after re-lease. |
| WWPP-R13.3 | Audit file path failure (R12.2 HCIA file / both) | Outbox + replay: PG path atomic; file writes via durable outbox with retry, replay, gap detection, alert; job continues. File-only keeps a local durable outbox first. |
| WWPP-R13.4 | Peer proximity default in M1 (R12.4) | "Keep all options in directors and also may follow the existing design doc for this p2p node stuff." Options on the Director page: LAN-only until RTT probe exists (UI marks proximity not available yet), build RTT in M1, server-only in M1. Follow the existing P2P design (P2P-Lite: CCGSC_MULTI_TENANT_WASM_APPLICATION_PLATFORM_TOP_LEVEL_DESIGN_V1.md, hand note, prime project CHANGELOG). Default not stated by owner: use the existing design doc's rule; if silent, LAN-only until RTT stays PROPOSED. |
| — | Native journal reconcile (ChatGPT Q5) | Not re-asked: commit 678493f7 (2026-11-11) decided native local job journal default = no local DB (memory + CSV/JSON); PG stays remote settlement authority. |

## Round 14 (2026-11-11 ~18:10Z, owner questions from WEBREVIEW-WWPP-V11, ChatGPT re-review)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R14.1 | Interim peer default in M1 (after R13.4) | Follow P2P-Lite design section 21.4 order as written (same device cache, same LAN, same public-IP cluster, lower RTT, success rate, server/CDN fallback); step 4 RTT is skipped until the RTT probe exists, then switches on. All R13.4 options stay on the Director page. |
| WWPP-R14.2 | M0 freeze vs per-lane result types | M0 freezes the contract; lane result types/hash encoding live in an unfrozen M0 appendix; per-lane fixtures (native reference vectors, 3 lanes) are mandatory before the first M1 executable acceptance; M3 stays independent parity validation. |
| WWPP-R14.3 | HCIA file-only audit crash order (after R13.3) | PG outbox row is truth: settlement writes an outbox row in the same PG transaction (PG stays settlement authority); the HCIA file is built from the outbox in sequence; after a crash replay from the last file tip hash. File-only mode still keeps a small PG outbox table. |

## Round 15 (2026-11-12 ~01:10 Dhaka, from WEBREVIEW-WWPP-V12, ChatGPT)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R15.1 | M1 P2P acceptance vs R13.4(c) server-only | Qualify always, activate by option: M1 always runs the P2P qualification tests (peer hit, fallback) in a test/staging setup; whether P2P is ON in the delivered config follows the Director option (a)/(b)/(c). Keeps R9.3 and R13.4(c). |

## Round 16 (2026-11-12 ~02:30 Dhaka, from WEBREVIEW-WWPP-V14 F4 + cmdc decision check)
| ID | Topic | Owner decision (MCQ) |
|---|---|---|
| WWPP-R16.1 | R10.1 wording (web worker thread = own OS thread) | Owner rule + labelled note: state the CCGSC architecture rule unqualified (each web worker thread runs on its own OS thread, private memory, no shared memory ever) plus one separate line labelled 'Implementation accuracy note: browsers do this in practice; the HTML spec does not mandate it.' |

## Next rounds (planned)
R2: job/task/result shapes, out-queue and result delivery (poll / webhook / WebSocket / download), review flow
(who approves, how), worker host glue model, verification mode, Node vs Go lane selector on the director page.

## Round 17 (2026-11-15, Dhaka night) - owner pool vision + Issue 100 re-pasted
Owner vision (verbatim): "Web workers are like threads and tabs are web worker hosts process ... hardwae/os/software/languages
threads and pricess implementation all.are different from each other, so my web worker pool will be my implementation of web
thread pool and my web browser tab wwHost pool may become process pool, both accessibile to remote online devs programmers for
science computing and assvely ebberrasively parallelbcalculation computing, which canbbe usen hybrid mode bybthe devs from local
and api reaource usages, the ccgscvserver only provides pools, the computaion calculation apps remains to the developers"
Owner ask: granular atomic grill MCQ sessions for pools, Admin page, Pools Director page, API design + usage + rules +
instructions, implementation vs hardware/software and existing similar web + native technologies; record reminders/issues/todos.
Issue 100 (hand note, re-pasted): one online cloud server, crowd machines, Software-defined Web Threadpool app; devs use current
thread-pool programming conventions + few added rules/modules/paths/endpoints; more servers later under one CCGSC umbrella;
now single-server pool, MSc MCSE MVP phase A only.
New vs decided (R1-R16): (1) TWO pool surfaces (web thread pool = workers, web process pool = tabs) vs one "Web Process Pool"
in R10.1; (2) pools-only server, apps = developers' (built-in Matrix/SSPrime/IsPrime become reference workloads, R3.2);
(3) thread-pool programming convention for devs (Issue 100) vs "TP out" (R3.2) - CONTRADICTION, ask; (4) hybrid local+API
(HYB G13-G16 LOCAL_FIRST/SIMULTANEOUS/ADAPTIVE exist); (5) multi-server umbrella later.
Prior-art table: agy lane -> F:\tmp\wwpp\PRIOR_ART_POOLS.md.
| ID | Topic | Owner answer (2026-11-16) |
|---|---|---|
| WWPP-R17.1 | Pool surfaces | **Two separate pool APIs**: web thread pool (web worker threads) and web process pool (tabs / wwHosts). How they share ids/core: R18. |
| WWPP-R17.2 | Dev convention / SDK | Owner asks: "do we need sdk? why or why not? can we just establish structure, architecture, organization, operation rules and standards and conventions for those API use for any language, framework, library?" Read API_Design_Policy_Discussions.md + CCGSC_BROWSER_STORAGE_READONLY_AUDIT_WO01.md (Q&A #6/#7), then grill (R18). |
| WWPP-R17.3 | Built-in Matrix/SSPrime/IsPrime | Answer text held BOTH "Reference workloads" and "Keep as built-in forever" - ambiguous, re-asked R18. |
| WWPP-R17.4 | Hybrid | Dev code handles local; pool API remote only. Dev app has local threads/processes + remote pools, picks LOCAL_FIRST / SIMULTANEOUS / ADAPTIVE; dev never touches server, server never touches dev machine; server provides 2+ APIs + rules. Owner asks "is that doable?" (yes - see chat). **New role, later:** user (consumer) and contributor are separate roles; a dev's own devices may join as **Dev-User-owner nodes**, reserved for that dev at P % (e.g. 50 %); more contributed resources = higher reserved P %. |
Prior art (agy, PRIOR_ART_POOLS.md): copy Future/result, map+chunksize, as_completed, cancel token; actors + MPI collectives poor fit (churn, private memory). Risks: untrusted results, churn, background throttling, mobile battery, OOM, stragglers, WASM float non-determinism, egress.
Contradiction found: storage Q&A #6 (2026-11-15) puts "short-lived task leases" in Redis; contract R3.3 keeps leases in PG authoritative. Asked R18.4.
| ID | Topic | Owner answer (2026-11-16) |
|---|---|---|
| WWPP-R18.1 | SDK | **No SDK first.** Ship contract + rules: OpenAPI 3.1 + JSON Schemas + "Pool Usage Conventions" doc + small examples in C++, Python, Node, Rust. Generated clients (openapi-generator) **later maybe**, after M1. Owner wants grill rounds with detailed discussion, definitions, explanations, examples. |
| WWPP-R18.2 | Two pools underneath | **Two route families, one core**: `/thread-pool/v1/...` + `/process-pool/v1/...`; shared job id space (R5.1), one PG truth, one lease/fencing engine, one audit. |
| WWPP-R18.3 | Built-ins | **Both**: Matrix / SSPrime / IsPrime stay built-in permanently (no retirement) AND are registered as reference developer workloads on the pools. |
| WWPP-R18.4 | Lease store | **Both as Director setting**: PG lease store and Redis-lease + PG-settle selectable (benchmark for thesis); default not set yet (PG proposed). Resolves storage Q&A #6 vs R3.3 contradiction as an option, not an override. |
| WWPP-R19.1 | Process pool unit | **Both A + B as Director setting**: A tab session lease (N tabs for T min, warm WASM/data, server relays) + B task-group pinning (group on one tab, all slots, then release). |
| WWPP-R19.2 | Thread pool call styles | All four in conventions doc: submit (one task), map (list + chunk size), as_completed result stream, cancel token / job cancel. Owner: implement per CCGSC_BROWSER_STORAGE_READONLY_AUDIT_WO01.md (storage tiers A/B/C, Q&A #6/#7). |
| WWPP-R19.3 | Lease store default | **Decide after M1 benchmark** (build both, measure on VPS; tests use PG meanwhile). |
| WWPP-R19.4 | MVP pages | All four: Owner/Admin, Pools Director (thread + process tabs), Developer portal, Contributor/worker page. Owner: read existing page docs, grill more, atomic. |
| WWPP-R20.1 | Director pages | **Both**: new `public/pools/director.html` (tabs Thread Pool / Process Pool; reuse login, worker-stats, HCIA audit view) AND hub `public/director-hub.html` linking Pools Director + each built-in director. Old Matrix/SSP directors untouched. |
| WWPP-R20.2 | Contributor page | **Both**: extend `worker.html` with pool mode (reuse current volunteers) AND new `public/pools/contribute.html` (tab registers once as wwHost; server assigns thread-pool vs process-pool work; contributor sets limits only). |
| WWPP-R20.3 | Origins | **Current subdomains as-is now**, and owner: "solve this problem, for now and for future, plan design implement too" -> staged hardening plan to Design V1 §13.3 (portal / worker / artifacts / apps-untrusted) asked R21.1. |
| WWPP-R20.4 | Developer keys | **All flows as options**: owner/admin issue keys manually on Admin page AND developer self-service now too; Director chooses: approve each request, or auto-approve all, etc. |
| WWPP-R21.1..R21.4 | Origin stages / multi-server ids / contributor role timing / doc home | **NOT ANSWERED** (owner re-pasted standing policy + Issue 100 instead, 2026-11-16). Recommended options stay PROPOSED: R21.1 three stages S0/S1/S2 behind flags; R21.2 serverId/poolId in ids now, one server; R21.3 roles now, P % later; R21.4 contract section + CR-43 row. Issue 100 "add to all related design docs" = R21.4, still open. |

## Round 22 (2026-11-16) - cluster/edge, dew, chain, P2P, cross-tab code + data sharing
Owner ask: pool must mimic cluster/edge computing, dew offline + late submit, hash/audit chain like a light blockchain, small P2P sharing data/code among pools, compile WASM/data/batches/shards/task lists on one worker and share to workers in same or other tabs.
Coverage answer given in chat: dew R9.2/R13.2 (M1), chain R8.2/R12.2, P2P R9.1/R14.1 (M1), compile-once-per-tab R10.2, data R10.3. Browser fact: compiled WebAssembly.Module posts only to workers of the same tab; other tabs share BYTES (Cache Storage/IndexedDB by hash) and compile again (browser code cache helps).
| ID | Topic | Owner answer |
|---|---|---|
| WWPP-R22.1 | Same-device multi-tab fetch | Leader tab per device (Web Locks election, bytes once into Cache Storage/IndexedDB by hash, BroadcastChannel announce, each tab compileStreaming) AND "each tab on its own" kept as options; on Director page and/or developer API. Owner: "is this server app, pools director decision and or coder devs user decision, decide that first then fix this" -> R23.1. |
| WWPP-R22.2 | Derived data | Hash-addressed derived artifacts: shareable worker output stored by SHA-256 (IndexedDB/OPFS), registered on server; same tab transfer, other tabs BroadcastChannel+IndexedDB, other devices P2P-Lite + server fallback; untrusted until verified like a result. |
| WWPP-R22.3 | Chain | Both options: per-job chain only AND + global Merkle checkpoints (periodic root over job-chain tips, each checkpoint hashes previous, optional export to git/file); selectable by Director and developer API -> authority rule R23.1. |
| WWPP-R22.4 | Edge hub | Design now, build after M1 (M2): edge hub role leases a batch from VPS, re-leases to LAN devices, collects + pre-verifies, late-submits (R9.2); VPS stays settlement authority. |
| WWPP-R23.1 | Decision authority | **3-layer rule for ALL settings**: Owner/Admin allow + limits; Director default per pool/job; Developer requests per job within allowed; server enforces; audit records chooser; each setting row gets "dev may override". All options on all selectors. Owner challenge: "answer verification and/or hash chain implementation is not server problem? server is the pools provider and allower" -> R24 (contradicts R5.2/R7.1/R11.2 server-side verification default; not overwritten, asked). |
| WWPP-R23.2 | Fetch mode authority | Director default leader-tab; dev may override per job. **Always warn devs about ineffective/inefficient pool usage by their code** (new rule: usage warnings). |
| WWPP-R23.3 | Chain mode authority | Both options: (a) Admin global Merkle on/off + dev raise-only on per-job chain; (b) dev free choice within allowed incl. weaker. Selectable. |
| WWPP-R23.4 | Shareable + edge hub | Dev marks shareable outputs in manifest; Director may disable per job; edge hub role granted by Owner/Admin (owner nodes first). Owner principle: **"make the web pools act similar to any machine pools, provide all thread and process pool features possible from CCGSC web worker pools, devs do all the rest."** |
| WWPP-R24.1 | Verification owner | **Both settings kept**: (a) server mechanisms + dev policy (k-of-n, recompute sampling, hash check, dev verifier WASM on another worker, verify locally after download; R5.2/R11.2 become DEFAULT values, not mandatory) AND (b) dev only, default none (results labelled 'unverified'). Selectable on server and by devs (R23.1 layers). |
| WWPP-R24.2 | Audit owner | **All audit optional**, incl. pool ops log. Owner principle (verbatim intent): "CCGSC only surely provides thread/web worker pool and ww host tabs each with multiple ww pools, or similar process pools, and all the storage facility and all other facilities associated; all other options are optional from the server; dev coder should adapt accordingly; but CCGSC server will try to provide compute worker pool, dew, p2p, hash chain facility options." **CONTRADICTION with R12.2 ("security log that cannot be turned off") - NOT overwritten; confirm in R25.** |
| WWPP-R24.3 | Classic pool features | **All**, in MVP scope: core executor (submit/map/as_completed/cancel, priorities, max parallelism, deadlines, retries, small DAG deps), process-pool (reserved hosts, warm initializer, slot recycle, affinity), cluster/edge (node labels/selectors, placement constraints, edge hub M2, dew, P2P), ops (quotas, fair share, metering, capacity view, efficiency warnings); plus "all other thread pool and process pool features of existing implementations possible on this platform - grill me if needed". |
| WWPP-R24.4 | Efficiency warnings | All four, all optional from server: API `warnings[]` field, developer portal job report, pre-submit lint (`POST /jobs:validate`), Director view of worst jobs. |
| WWPP-R25.1 | Security log | **Security log always on** (auth, keys, admin setting changes, approvals, revocations); every other audit optional (R24.2). Resolves R12.2 vs R24.2. |
| WWPP-R25.2 | Scheduling | All four, 3-layer selectable, "if possible from this system, make the app that way": FIFO + priorities (aging), fair share between devs, locality/affinity aware, speculative stragglers. |
| WWPP-R25.3 | Queue + lifecycle | All four: bounded queue + rejection policy (reject 429 / block-timeout / drop-oldest / caller-runs), shutdown vs shutdownNow, elastic process-pool size (min/max hosts), per-worker initializer state. Owner: Python, C/C++, Go, Rust, C#, PHP, Java, Node styles allowed for both pools, only through the APIs, "too complicated?" -> answered in chat: no; one neutral concept set on the wire, a per-language idiom mapping table in the conventions doc, examples for C++/Python/Node/Rust (R18.1). |
| WWPP-R25.4 | Patterns | **All patterns in MVP**: map-reduce, DAG, pipelines, actors. Never MPI collectives / shared memory (R10.0). Scope: massively parallel scientific computing, simulation, modelling, calculation, math only; general-programming and unrelated features stay out of MVP. RISK noted: large M1 scope - propose staged order inside MVP (R26). |
| WWPP-R26.1 | Build order inside MVP | **All at once** (owner 2026-11-17): every pattern of R25.4 (core pool, map-reduce, DAG, pipelines, actors) in one MVP milestone; no staged order. Risk note R25.4 stays recorded. Source of question: hermes review `F:\tmp\wwpp\review_s17_hermes.md` (2026-11-17). |
| WWPP-R26.2 | Audit hash chain default | **OFF by default**; security log only (R25.1); chain opt-in. Owner condition: lightweight hash chain / lightweight blockchain without consensus is an MSc MCSE thesis requirement, so it **must be built and tested before MVP completes**. Owner asked who turns it on (server, dev or both) -> grill R27.1. Supersedes the R8.2 default "per-job chain" (R8.2 kept as option). |
| WWPP-R26.3 | Verification default | **(a) server sampling** default (first task per worker + 5 %, R11.2), controlled on Director pages; dev may switch to **(b)** dev-only, results marked unverified, dev uses any method. Owner principle restated: CCGSC MUST provide thread pool + process (wwHost) pools like existing pools, offline/late result submission to server and on to devs, P2P option, optional hash chain capacity, language-similar features for massively parallel jobs; everything else optional ("the more the better") but MSc MCSE thesis MVP first. |
| WWPP-R26.4 | Id shape | **serverId + poolId in ids now**: `<serverId>.<poolId>.<jobId>`, serverId fixed `s1` on the single server (Issue 100). Closes R21.2. |
| WWPP-R27.1 | Chain control + page roles | **Correction by owner (2026-11-17):** Director pages are NOT pool pages. Director pages = control dashboards for the 3 existing server apps (Matrix, SSPrime, IsPrime: work, task, test control); 3 new pool-using Director pages maybe later, **not now**. The two pools are controlled by **2 pool Admin pages** (Thread Pool Admin, Process Pool Admin) + **developer users**. Chain (and every pool setting): Pool Admin allows + sets limits + sets pool default (chain default OFF, R26.2); Developer requests per job within what is allowed; server enforces and writes the chain. Server API must state its capabilities and capacity clearly to devs (capabilities endpoint + docs). **Supersedes** page names in R19.4/R20.1 (`pools/director.html`, `director-hub.html` -> pool admin pages) and the "Director" layer of R23.1 for pools. Contradiction recorded, old rows kept. |
| WWPP-R27.2 | Thesis tests for chain + pools | **All**, must pass before MVP complete: tamper detection, overhead benchmark (chain OFF vs ON), offline/late submission ordering, P2P / Merkle checkpoint re-verify; plus speed, performance, benchmarks of all options, comparison with similar systems (HPC, GPU, GPU servers, local hardware alternatives), theory, equations, applications, novelty, proof of concept, international-standard MSc MCSE thesis data. Run in parallel where possible, else serial. |
| WWPP-R21.3 | Contributor role timing | **Both roles now, P % later** (owner 2026-11-17): developer user and contributor are separate roles with separate keys/consent in MVP; reserved P % capacity for developer-owned nodes in M2. |
| WWPP-R21.4 | Doc home | **All three, interlinked** (owner 2026-11-17): (1) new standalone `CCGSC_TWO_POOLS_DESIGN.md` in vault CCGSC_SystemDesignREADME = full design home; (2) small WW-PP contract §17 summary + pointer; (3) small CCGS_WASM repo `docs/` note next to code + pointer. Plus pointer rows in Design V1 §13.3/§19, CCGSC_Final_Unified_Hand_Note (Issue 100), CCGSC_OPEN_ITEMS_REMINDERS CR-43, storage audit doc, thesis research index. All linked to and from each other, indexed. Vault edits via Obsidian with diff shown first (A81a). |
| WWPP-R21.1a | Origins: facts asked | Owner asked: do we need another domain; does server run dev code; don't we already have separate origins. Measured on VPS nginx 2026-11-17: 3 server_names already: `ccgsc.digitalbd.org`, `ccgsc-demo.digitalbd.org`, `ccgsc-director.digitalbd.org` (3 separate origins). Answer in chat + grill R28. |
| WWPP-R28.1 | Dev code in contributor browsers | **Allow developer WASM + any JS + JSON** (owner: needed for a real crowd-cloud platform); isolate it with a **sandbox origin** (4th origin, e.g. `ccgsc-run.digitalbd.org`, sandboxed iframe, no cookies, own empty storage). Owner asked how it works, overheads, can one tab run both, what origins are -> answered in chat 2026-11-17 + R29. Server still never runs dev code (R17.4). |
| WWPP-R28.2 | Pool Admin pages | `ccgsc-director.digitalbd.org/pools/...`, public + login now; IP/Tailscale allow-list later as a flag. Owner add: a public "for developers" section on the demo worker page; login required; developers request API keys there (see R29.2 for the conflict with R28.3). |
| WWPP-R28.3 | Developer portal host | **Developers on own host, contributors on demo**: developer portal + API keys on new host owner-named `ccgsc.dev-users.digitalbd.org` (name to confirm R29.3); contributors/workers on ccgsc-demo. Links to `ccgsc.digitalbd.org` and to the developer portal on every worker, director and public page. CONTRADICTION with R28.2 "request API on demo worker page" -> R29.2. |
| WWPP-R28.4 | Dev section on demo page | **Owner 2026-11-17: not needed now** - demo worker page gets only the **link** to the developer portal (and to ccgsc.digitalbd.org). API login/keys only on the developer portal host (R28.3). Resolves the R28.2 vs R28.3 conflict. |
| WWPP-R29.1 | Sandbox form | **4th host `ccgsc-run.digitalbd.org` + opaque sandbox** (iframe `sandbox="allow-scripts"` without allow-same-origin): dev WASM/JS/JSON gets a null origin, no cookies, no persistent storage; demo page caches WASM bytes by hash and passes them in via postMessage (transfer). Host-only cookies on all hosts. Run-origin CSP connect-src enforces dev network policy (R5.4). Spike test needed: workers inside opaque iframe on Chrome/Firefox/Android. Several workers per tab allowed inside the iframe. |
| WWPP-R29.2 | Developer portal name | **`ccgsc-dev.digitalbd.org`** (replaces owner draft `ccgsc.dev-users.digitalbd.org`). Links to it + `ccgsc.digitalbd.org` on every worker, director, public page. |
| WWPP-R29.3 | Host build timing | **In parallel with pool code**: code on localhost origins, real hosts (DNS by owner, nginx staged copy, certs, smoke, owner GO for live) before MVP test. cups/ufw untouched. |
| WWPP-R29.4 | Workers per tab default | **Auto = cores - 1, min 1, max 4 at the auto default**; Pool Admin (developer/user director setting) may change within range; Server Admin may allow more where more cores exist. Owner asked: why could a contributor laptop get slow and how to minimise; phones also have many cores -> answered in chat, proposals for R30. Supersedes default of R10.4 (options kept). |
| WWPP-R30.1 | Contributor device guards | **All four as options on the Pool Admin pages**: CPU slider (Low/Medium/Full), auto back-off, battery rule, memory cap per worker. Everything configurable + on/off from server/admin, future-proof; a guard the browser cannot support simply does not apply. Auto back-off **only when the device is very very busy** (admin-controlled; contributor control maybe later, not now). **NOT** pause on hidden tab: hidden/background tabs must keep working; stop tabs sleeping; reuse the existing worker page + server settings techniques. Owner: keep this grill style for pools, API design and other CCGSC features. |
| WWPP-R30.2 | Phone defaults | **1 compute worker on battery, 2 when charging + screen on**; stop under 20 % battery or when hot (slowdown detected). Owner asked: 2 or 3 origins in one tab? and a separate worker for non-compute upkeep plus 1-2 compute workers? -> R31.1. |
| WWPP-R30.3 | When work runs | **Any time the tab is open, also hidden/background/sleep-prone** (keep alive) + guards. Plus a **no-browser contributor app**: Go exe with headless browser, contributor schedule (hours), may run as background watcher or merge into the existing CCGSC browser watcher. |
| WWPP-R31.1 | Upkeep worker | **1 upkeep (control) web worker on the demo origin + N compute workers in the run iframe**. Upkeep = heartbeat, lease renew, task fetch, result upload; not counted as compute. One tab = 2 origins (demo top page + opaque run iframe); ccgsc.digitalbd.org and ccgsc-dev are links only. |
| WWPP-R31.2 | Keep-alive | **All four**: reuse existing worker.html methods first; Web Lock + open WebSocket/SSE; Screen Wake Lock on phones (user tap); lease recovery when frozen anyway. Plus: search existing code + plan docs for other good methods now/future (DRY KISS) - audit task for a lane. |
| WWPP-R29.1-SPIKE | Sandbox spike result | **PASS 2026-11-17** (headless Edge, CONTROL-01, two local origins 127.0.0.1:8701 parent / :8702 runner, iframe `sandbox="allow-scripts"`): iframe origin = `null`; localStorage, indexedDB and cookie all **blocked**; blob-URL web workers start inside the opaque iframe; WASM bytes passed in by transfer, compiled once, Module posted to 4 workers; 8 tasks x sum(20,000,000) correct in 173.9 ms; message round trip avg **54 us**. Files `F:\tmp\wwpp\spike_sandbox\` (parent.html, runner.html, serve.py, cdp_run.mjs). Not yet run: Chrome, Firefox, Android. Built-in browser pane could not load the 2nd local port (pane limit). |
| WWPP-R29.3-BUILD | New hosts built (owner GO 2026-11-17) | DWH DNS (cPanel uapi mass_edit_zone, serial 2026102800 -> 2026111700): A `ccgsc-dev` + `ccgsc-run` -> 75.119.129.161 TTL 300; zone backup `F:\tmp\wwpp\hosts_build\zone_backup_20261117.json`. VPS nginx: new `/etc/nginx/sites-available/ccgsc-dev-run` (static only; dev = placeholder portal `/var/www/ccgsc-dev/index.html`; run = `/var/www/ccgsc-run/runner.html` spike runner, CSP script-src self+inline+wasm-unsafe-eval+blob, worker-src blob, connect-src none, frame-ancestors only ccgsc-demo, no cookies); backup `/root/nginx_bak_20261117T073257Z`; nginx -t ok; certbot --nginx cert for both names; certbot.timer active (twice daily). Smoke: all 5 hosts https 200, http->https 301. Stuck `certbot renew --dry-run` (6+ min) killed. Rollback: remove sites-enabled symlink + reload, delete 2 DNS records. |
| WWPP-R31.2-AUDIT | Keep-alive audit (hermes, 2026-11-17) | `F:\tmp\wwpp\KEEPALIVE_AUDIT.md`: 18 existing mechanisms; reuse worker-thread lease heartbeat (prime_segment_worker.js:283-308, matrix_compute_worker.js:162-186), dead-lease recovery (matrix_app.js:435-444), sendBeacon/keepalive POST, server surrenderTask, WS reconnect; NOT present: wakeLock, held Web Lock, SSE, freeze/resume, sandbox iframe. Main-thread heartbeats die in hidden tabs -> confirms R31.1 upkeep worker. Bug found: worker.html:479 wake nudge is a no-op. |
| WWPP-R29.1-SPIKE2 | Sandbox spike, all browsers (2026-11-17) | **PASS on all 4**: Edge (CDP, 4 workers, 173.9 ms, 54 us round trip), Chrome desktop (CDP, 180.7 ms, 80 us), Firefox 160 headless (44.0 ms, 60 us; result reported via `/report` GET in server log), Android 10 Chrome 157 on OPPO cph2185 (opened by `am start` over SSH, server on CONTROL-01 tailnet IP; 3 runs 214-482 ms, round trip 436-584 us). Phone round trip ~8x desktop: batch small tasks on phones. Spike server stopped after each run. |
| WWPP-R31.2-FIX | worker.html wake nudge | Fixed + pushed (Webassembly `9b336d6e`): removed no-op nudge and per-wake `pool.on('noop')` listener leak; real nudge = WorkerPool.js `send({t:'hb'})`. Not deployed to VPS yet. Missing keep-alive pieces (wakeLock, held Web Lock, SSE, freeze/resume) go into the pool upkeep worker build (R31.1/R31.2), not the old worker page. |
| WWPP-R32.1 | API style | **REST + SSE + gRPC, all now** (owner 2026-11-17): REST/JSON for commands, Server-Sent Events stream for live events, gRPC for high-volume C++/Go/Rust developer clients - for BOTH pool APIs (`/thread-pool/v1`, `/process-pool/v1`). OpenAPI 3.1 + .proto as the two contracts. Note: gRPC is developer-to-server only; contributor browsers keep HTTPS/WebSocket. |
| WWPP-R32.2 | Job model | **Both: one job model + session API**: POST /jobs (pool thread/process, module hash, tasks or split spec, options; process pool adds groupBy/pin) for everyone, plus explicit process-pool session API (open session, stream tasks, close) for power users. One job id space (R18.2, R26.4). |
| WWPP-R32.3 | Results delivery | **All four**: GET results with cursor (as_completed order, late results OK), SSE `task.done` per result (small inline, big as URL), signed webhook to developer server (with retries), bulk download (zstd/tar by hash) at job end. |
| T0-ADD-20261117 | Tier 0 additions | Owner approved all 4 groups from `F:\tmp\tier0_inventory\TIER0_CANDIDATES.md` (build/debug/profile, data/DB/JSON, ops/network/bench, lang/deps/format). Standing taste: with permission, regularly review/calibrate/add new tools for best AI SDLC work. |
| WWPP-R33.1 | Pool GPU (WebGPU) | **GPU as a worker capability in both pools** (owner 2026-11-17): tab reports {webgpu, adapter, limits}; job declares cpu / gpu / gpu-preferred and ships WGSL + JS or WASM + WGSL; scheduler sends GPU tasks only to GPU tabs; Pool Admin on/off + limits (time, memory, GPU only when charging on phones/laptops); GPU results verified by sampling (R26.3). No separate GPU pool. Test WebGPU inside opaque sandbox iframe (spike). Tracking: new CR-47 + note on CR-25 (owner "both"). Links WP-G1, R27.2 thesis GPU comparison. |
| WWPP-R32.4 | Developer auth | **Now: API key + short token** (key shown once, hashed, revocable, scoped; exchanged for 15-min JWT used on REST/SSE/gRPC). **Later (planned now): optional OAuth2 client credentials** (token endpoint, future SSO). |
| WWPP-R32.5 | Limits + fairness | **All four**: per-key quotas (running jobs, queued tasks, task size/time, req/min; 429 + Retry-After), fair-share scheduler, priority classes (high only if admin allows), credits/metering (CPU/GPU-seconds, reports/rewards, no payment now). All controlled from the server + a new **API control dashboard admin page** under the admin login umbrella (ccgsc-director). **Nav rule:** all public, admin and director pages share one all-in-one nav bar; public links always visible; private links appear after admin login. Logins separate: admin, contributor (optional), developer API user. |
| WWPP-R32.6 | Versioning | **URL major + contract tests**: /thread-pool/v1, /process-pool/v1; additive-only within v1; deprecation header + 6-month notice before v2; OpenAPI + .proto in repo; contract tests generated from OpenAPI on every change. |
| WWPP-R32.7 | Code/data upload | **Both**: content-addressed artifacts by sha256 (PUT /artifacts, resumable chunked upload for big data, cached by hash in tabs) as default; small inline (< 256 KiB) for quick tests. Owner frame for all API choices: safe, sound, DRY KISS MSc MCSE MVP, future-proof and feature-rich. |
| WWPP-R32.8 | Error model | **RFC 9457 problem+json** {type, title, status, detail, code, traceId, retryable}; same codes on REST, SSE and gRPC status details; one documented code list. |
| WWPP-R32.9 | Task failure defaults | **All four** (dev may override within admin limits): retry + reassign on lease expiry (max 3 attempts default); job policy partial / fail-fast / threshold (R11.1); poison-task quarantine after 3 worker crashes; dead-letter store for late results after job end (dew R9.2). |
| WWPP-R34.1 | Batching rule | **Owner 2026-11-17: all tasks batched on all devices wherever possible, by the developer users** (massively / embarrassingly parallel pools). API: job/task batch size field + map chunksize (R19.2), server default batch per device class, warnings[] when tasks are too small (R24.4), docs + examples show batching. Spike data: message round trip 54-80 us desktop, 436-584 us Android. |
| WWPP-R31.2-FIX2 | worker.html legacy record | Owner: keep removed lines as comments; done (Webassembly `27589438`). |
| WWPP-R35.1 | Result re-run checks | **Optional re-run %, developer-led**: server re-sends N % of tasks to other workers (default 1 %, dev 0-100 %); server compares exact hashes only (never runs dev code); tolerance compare via dev comparator on a worker; deeper checks by dev. **All options live on Pool Admin**; developer can only request (API console, code, API call); an option switched off by admin is simply unavailable. Supersedes R26.3 default (options kept). Owner asked: WASM/JS/JSON validation, P2P, dew cases -> R36. |
| WWPP-R35.2 | Tamper evidence | **Both options on Pool Admin**: (a) server minimal = security log + sha256 of every accepted result with ids, optional per-job chain / Merkle; (b) everything opt-in. Dev requests/agrees; admin-off = unavailable. Full chain, signatures, independent verification = developer side via API exports. Principle restated: pool APIs provide pools; developer local code is the developer's concern. Owner asked "are there 3 CCGSC APIs now?" -> answered in chat. |
| WWPP-R36.1 | Upload checks on dev code | **All four, all optional + configurable by server admins**: format/size/hash, import/permission scan, review gate for new code, malware/abuse scan. Owner: we will build deterministic tools plus free/paid AI checkers; bad developer users warned, barred, banned, fined later. Answer recorded: still **2 pool APIs** (thread-pool, process-pool) on one core; gRPC = second transport, not a 3rd API; legacy Matrix/SSPrime/IsPrime endpoints separate. |
| WWPP-R36.2 | P2P + dew trust | Hash-addressed sharing only (peers share server-published sha256 artifacts/inputs; results always to server); dew signed lease receipts (task ids, input hashes, deadline; **24 h window default, dew default ON**); late-result policy (dead-letter / extra sample, no double credit). **P2P default: owner text says "default on until tested" while the option label says "off by default" - CONTRADICTION, recorded; proposed reading: admin option, ON in test builds, confirm before public.** Owner: all previously discussed features go in MVP; reuse existing design/plan/code snippets; DRY KISS. |

| WWPP-R37.1 | Tamper detection MVP | 2026-11-17 | ALL 4: hash check on every load (incl. IndexedDB cache hits), random re-run % (R35.1), signed task envelope (Ed25519 over jobId/taskId/artifact sha256/input sha256/lease), canary known-answer tasks + reputation (warn/bar/ban). All Pool Admin options. Owner add: MEASURE overhead, speed, performance, benchmarks, effectiveness, efficiency, accuracy of these AND all CCGSC features always (MSc MCSE thesis proof-of-concept) | decided | thesis test list §14 |
| WWPP-R38.1 | Pool admin pages | 2026-11-17 | One admin app /pools/admin on ccgsc-director, tabs Thread Pool / Process Pool / API (keys, quotas, metering) / Common (tamper, P2P, dew). Owner open question: do devs need separate Threads API / Processes API beside the 2 pool APIs? (see R38.4) | decided + open sub-q | - |
| WWPP-R38.2 | Shared nav bar | 2026-11-17 | One nav.js + nav.json on every host; links tagged public/contributor/dev/admin; login state shows private links | decided | - |
| WWPP-R38.3 | Logins / roles | 2026-11-17 | Owner custom: 4 roles (admin, director, contributor, developer), 2 account stores: store A = admin + director (shared, separate from users), store B = contributor + developer (shared); role flags, one person may hold several roles in a store; each host shows only its own login | decided | - |
| WWPP-R38.4 | Threads/Processes API? | 2026-11-17 | NO separate Threads or Processes API. Keep 2 pool APIs; publish idiom table only (Python concurrent.futures/multiprocessing, C++ std::async, Go errgroup/channels, Rust rayon, Node Promise.all, Java/C#/PHP) mapping each idiom to REST calls + examples; no adapter code. Reason: native threads need shared memory (forbidden) + internet latency | decided | R19 language styles |
| WWPP-R37.2 | Thesis measurement | 2026-11-17 | BOTH: built-in light metrics always on (per-feature timings/counters in one PG metrics table, Pool Admin on/off) + heavy benchmark harness for owner-started thesis runs along/beside MVP | decided | R37.1 |
| WWPP-DRAFT-CHK-20261117 | API drafts check | 2026-11-17 | process_pool_v1.openapi.yaml 3.1 valid, 14 paths, 128 refs all resolve; ccgsc_pools_v1.proto 721 lines, braces 58/58, services ThreadPool+ProcessPool, 25 rpcs; protoc not installed (compile check pending) | done | F:	mp\wwpppi |
| WWPP-R38.5 | Admin page set | 2026-11-17 | SUPERSEDES R38.1 single-app/tabs layout (owner custom): separate pages - (a) one combined simple MONITOR page for pools + APIs, linking to settings sections on the control pages; (b) Thread/WW Pool admin page; (c) Process/wwHost Pool admin page; (d) Thread Pool API admin page; (e) Process Pool API admin page; each with details. Every button/option with legend + instructions; every page has a manual section or manual page. Owner text also says "1 combined simple api admin page for monitoring all api" - CHECK whether this is the same page as (a) or a 2nd monitor page | decided, 1 sub-q | R38.1 |
| WWPP-R38.6 | Admin apply rules | 2026-11-17 | Versioned config + audit: each Save = new version (who/when/what/reason), new jobs use it at once, running jobs keep theirs, one-click rollback; security log always on | decided | R22 |
| WWPP-R36.2-RES | P2P default resolved | 2026-11-17 | Owner: P2P ON everywhere (until tested; admin option, can switch off). Resolves the R36.2 contradiction | decided | R36.2 |
| WWPP-R38.5b | Monitor page tiers | 2026-11-17 | 3 tiers: (1) combined brief Monitor page (pools + both APIs, simple error/warning messages); (2) medium monitor pages: one pool monitor + one simple API monitor (both APIs) with bigger error messages, dashboards and control options; (3) full detail on the individual admin pages (b)-(e). Every section links to its matching section on the admin pages | decided | R38.5 |
| WWPP-DOC-FLOW-20261117 | Design doc flow | 2026-11-17 | Draft stays in F:\tmp\wwpp\draft; agy (high) reviews first (xAI lane blocked); Codex/Copilot fix only if needed; owner then approves; then vault copy via Obsidian + contract section 17 summary + repo docs note + links to 4 Software-Defined_Web_Thread-Pool notes | decided | R21.4 |
| WWPP-R37.1b | Tamper measures config | 2026-11-17 | Owner: all 4 tamper measures (hash check, re-run %, signed envelope, canary + reputation) are controlled and configured on the admin pages (on/off + parameters each) | decided | R37.1 |
| WWPP-R38.7 | Config version retention | 2026-11-17 | Owner: "dont need too many versions". Applied: keep last 10 versions per admin page (count is itself an admin setting) + any version a running job uses + admin-named pins (e.g. thesis runs); older versions pruned; the who/when/what audit row stays in the security log | decided (default 10 by head AI from owner direction; owner may change) | R38.6 |
| WWPP-R39.1 | Review fixes | 2026-11-17 | Owner: "Fix 25 findings" = approve all agy + xAI review fixes; PROPOSED tags become APPROVED (WWPP-R39.1). Partial rejection kept only where a fix would override an earlier owner decision (pre-MVP IP gate) | decided | review_agy.md, review_xai.md |
| WWPP-R39.2 | Admin access extra paths | 2026-11-17 | Owner: admin pages stay public + login (R28.2) AND are ALSO reachable over Tailscale / IP allowlist until MVP; owner also named mstsc (RDP) - read as admin work via RDP to owner machines; exact nginx allow rules TBD at build | decided, details TBD | R28.2 |
| WWPP-PROTOC-20261117 | proto compile check | 2026-11-17 | ccgsc_pools_v1.proto compiled OK on lab-node-02 (grpcio-tools, libprotoc 35.1, tmp venv; pb2 + pb2_grpc generated; venv + outputs deleted after) | done | xai 6 |
| WWPP-R21.4-DONE | Doc home built | 2026-11-17 | Vault CCGSC_TWO_POOLS_DESIGN.md created via Obsidian (= draft v4); contract section 17 pointer appended (tmp); repo docs/TWO_POOLS_DESIGN_POINTER.md commit cb2d3c7c pushed; marker-guarded links appended to 4 Software-Defined_Web_Thread-Pool notes; back-links in design doc before section 21 | done | R21.4 |
| WWPP-R40.1 | Monitor page numbers | 2026-11-17 | All 4 groups on brief combined Monitor: Capacity (contributors, tabs, CPU/GPU workers, free slots per pool), Work (jobs running/queued/failed, tasks/s, avg task time), Trust (hash fails, re-run mismatches, canary fails, quarantined, banned, 24 h), API + system in brief (req/min, 4xx/5xx, 429, p95; server CPU/RAM/disk, PG + SSE conns); each number links to detail | decided | R38.5b |
| WWPP-R40.2 | Config storage | 2026-11-17 | PG table pool_config_version (id PK, page, version, json, json_schema_ver, author FK, reason, created_at, pinned_name, in_use) + JSON Schema validates every Save | decided | R38.6, R38.7, R-97 |
| WWPP-R40.3 | Admin manuals | 2026-11-17 | Both: (?) legend tooltip per button/option + short manual section at page bottom + full manual page /pools/manual/<page>.html built from one markdown source in repo | decided | R38.5 |
| WWPP-SPIKE2-20261117 | Sandbox spike v2 | 2026-11-17 | PASS Edge (CONTROL-01) + Chromium + Firefox + WebKit (lab-node-02 Playwright): MessageChannel handshake (forged msg ignored), compile from bytes 0.7-1.7 ms, sha256 in iframe ~1 ms/MB + tamper reject, Ed25519 sign/verify 0.4 ms + tamper reject, worker cap, Web Lock leader; WebGPU SKIP (headless no adapter; Firefox/WebKit no navigator.gpu) | done | R39.1 |
| WWPP-R41.1 | MVP stack (A116) | 2026-11-17 | New Go service pools-core beside Node: /thread-pool/v1 + /process-pool/v1 + SSE + admin/monitor pages, own PG tables, nginx; Node apps untouched now; LATER wave ports Matrix/SSPrime/IsPrime onto Go. CMD double-click launcher + pwsh 7 TUI menu; full debug JSON-line logs | decided | A116 |
| WWPP-R41.2 | MVP build order | 2026-11-17 | Owner: everything at once (all MVP features in one build), debug mode | decided | A116, A5 |
| WWPP-R41.3 | SDLC mode | 2026-11-17 | Fast mode for local + lab nodes (build + go test + e2e per batch, one review per wave, green = commit + push main); Safe mode (full review + owner OK) for VPS staging and live | decided | A107, A116 |
| WWPP-R41.4 | PG schema test host | 2026-11-17 | PostgreSQL on lab-node-02, localhost only, test DB, service stopped when idle | decided | T6 |
| WWPP-R42.1 | Final MVP wave scope | 2026-11-19 | Owner MCQ: core gaps (token deny-list, metrics table/routes, Redis lease + PG settle) + admin UI controls + VPS staged copy + smoke + GPU and P2P/dew verify, all in one wave (WO_MVPW_SPLIT.md, 6 slices) | decided | T81 |
| WWPP-R42.2 | Sandbox timing | 2026-11-19 | Owner: "we need that" - the run-origin sandbox wiring (opaque iframe + run-host CSP, R29.1) moves INTO the MVP wave (slice E). Supersedes the timing in R11.3 ("sandbox after MVP"); R11.3 stays as history. The 4th host DNS + nginx is a VPS change and still needs the owner GO | decided | T81, R11.3, R29.1 |
| WWPP-R42.3 | GLM use | 2026-11-19 | Owner MCQ: 3 Z.ai accounts review the merged wave, 3 folders (glm1/glm2/glm3), one slice group each | decided | T81 |
| WWPP-R42.4 | GPU choice in worker tabs | 2026-11-19 | Owner: worker tabs (WTP workers, WPP tabs, worker.html/js) use a GPU by default; try all, strongest first, then weaker, then browser default, then CPU. Done for matmul-f32-gpu; shared picker for all GPU kinds open | decided | T82, CR-48 |
| WWPP-R42.5 | CPU + GPU together | 2026-11-19 | Owner: contributor tabs use all CPU cores and all GPUs at once where exposed (iGPU + dGPU in parallel); else CPU + strongest GPU, then CPU + weaker GPU, then CPU only | decided | T83, CR-49, CR-49a |
| WWPP-R42.6 | CPU + GPU limits (amends R42.5) | 2026-11-19 | Owner: "Use CPU and all GPUs both within safe limits or already agreed limits, else CPU plus the strongest GPU, then the weaker one - all within safe limits or already agreed limits". Agreed limits that bind it: R10.4/R29.4 workers per tab (auto = cores - 1, min 1, max 4 at auto default, never above server cap or device CPUs); R30.1 device guards (CPU slider Low/Medium/Full, auto back-off, battery rule, memory cap per worker); R30.2 phones (1 worker on battery, 2 charging + screen on, stop under 20 % battery or hot); R33.1 GPU (Pool Admin on/off + time/memory limits, GPU only when charging on phones/laptops); R12.3 per-task 30000 ms / 256 MiB (proposed, measure first); R32.5 quotas. Where no limit is agreed yet, the safe default is the lower option and the setting is asked as an MCQ | decided | T83, CR-49b |
| WWPP-R42.7 | Live DB + Redis | 2026-11-19 | Owner: SQLite now, PostgreSQL later. Redis (asked how to use it safely): keep the default leaseStore=pg; Redis later only for leases/liveness (R3.3, R18.4), never the result authority; on the shared VPS Redis use its own logical DB index + ACL user with password + key prefix pools:, localhost only, maxmemory policy checked so Node app keys are never evicted; client needs AUTH/SELECT support first | decided | T86 |
| WWPP-R42.7a | Redis now + passwords | 2026-11-19 | Owner: "wanted to use redis from get go ... use redis then"; "give password to all local guys, dont lose it"; PG settle live too; "Go 1-5, go all". Done T90: pools-core on PostgreSQL + leaseStore=redis (ACL user pools, pools:*, DB 1), default user password for pool_admin/pool_gateway, secrets /root/ccgsc_secrets + owner copy | decided, live | T90 |
| WWPP-R42.8 | Redis memory + crash safety | 2026-11-19 | Owner: keep ALL options selectable on the combined Director page: memory cap fixed 1-4 GB or dynamic 10-40 % of free RAM, policy noeviction; persistence AOF everysec or snapshots only; no swap; keep Node pools, bridge later (Go monitor reads workers:info read-only). Live default now 1 GB + AOF everysec (head, 2026-11-19); Director controls = T91 (Codex). Redis "virtual memory" feature was removed in Redis 2.6, not available in 7.0 | decided | T91 |
| WWPP-R42.9 | Contributor login | 2026-11-20 | Owner: "contributors should be able to join without id password, that was the plan. only dev coders needed id pass login auth". Contributor tabs get an anonymous per-tab token (rate-limited, low trust, results verified by canaries + re-runs); developer/admin/director keep login. Contradiction with R38.3 (contributor in store B) resolved: store-B contributor account becomes optional, for credit later | decided | T96 |
| WWPP-R42.10 | Root contributor page | 2026-11-20 | Owner MCQ: one page joins both - https://ccgsc-demo.digitalbd.org root worker page keeps the old Node lanes AND registers with pools-core (anonymous token, T96) in the same tab, splitting its workers between both within the agreed CPU/GPU limits; /pools/contribute.html stays as direct link | decided | T103 |
