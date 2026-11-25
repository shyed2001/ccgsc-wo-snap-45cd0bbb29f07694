/* docs/ccgsc_abi.h - the generic CCGSC WASM ABI (D-W10-10 / W10d).
 *
 * ONE contract, every wasm32 language. A workload module exports its memory and
 * exactly three functions:
 *
 *     unsigned  ccgsc_alloc(unsigned len);              bytes reserved for input
 *     int       ccgsc_run(unsigned inPtr, unsigned inLen);   output length
 *     unsigned  ccgsc_out(void);                        output byte pointer
 *
 * The platform copies the task input bytes to ccgsc_alloc(inLen), calls
 * ccgsc_run(inPtr, inLen), and reads ccgsc_run's NON-NEGATIVE return as an
 * output length from ccgsc_out(). A NEGATIVE return is a workload error code:
 * the task fails and the platform reports the code, it never interprets it.
 *
 * There is no other channel. A module imports NOTHING (the only tolerated import
 * is env.abort, a stub that throws), so there is no host call, no clock, no
 * random source and no file system. System parallelism comes from the pools;
 * threads inside one task are out of scope (CR-56).
 *
 * Pointers are wasm32 linear-memory offsets, so `unsigned` is the wire width.
 * Report them through a cast, never through a host pointer type: this header is
 * included by the workload, and the workload is the only thing that owns
 * memory.
 *
 * This file is DOCS LAYER. It is not the platform's implementation and it is not
 * included by pages/js; the normative description lives in docs/WASM_ABI.md.
 */
#ifndef CCGSC_ABI_H
#define CCGSC_ABI_H

#ifdef __cplusplus
extern "C" {
#endif

#if defined(__wasm__)
#define CCGSC_EXPORT(name) __attribute__((export_name(#name)))
#else
#define CCGSC_EXPORT(name)
#endif

/* Allocate (or simply return) a buffer of at least `len` bytes for the input.
 * Return 0 when the request cannot be served; the platform treats 0 as a
 * workload failure, because byte 0 is the module's own static data. */
CCGSC_EXPORT(ccgsc_alloc) unsigned ccgsc_alloc(unsigned len);

/* Run one task. `inPtr` addresses `inLen` input bytes the platform wrote after
 * ccgsc_alloc. Return the output length in bytes (>= 0) or a negative error
 * code of the module's own choosing.
 *
 * A module MUST be a pure function of (inPtr, inLen): the same input bytes
 * always produce the same output bytes, because the platform records the SHA-256
 * of those bytes as the task result. */
CCGSC_EXPORT(ccgsc_run) int ccgsc_run(unsigned inPtr, unsigned inLen);

/* Return the offset of the first output byte. Valid until the next call. */
CCGSC_EXPORT(ccgsc_out) unsigned ccgsc_out(void);

#ifdef __cplusplus
}
#endif

#endif /* CCGSC_ABI_H */
