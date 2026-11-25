// Web/workloads/matmul_wasm/matmul.c | W10d D-W10-10 example workload (workload layer).
//
// The generic C ABI of docs/ccgsc_abi.h: bytes in, bytes out, nothing else.
// No stdlib, no allocator, no I/O, no clock, no RNG: the module declares one
// static buffer region and the platform hashes the bytes it reads back.
//
// Input  : little-endian u32 header n, then n*n f32 A, then n*n f32 B.
// Output : n*n f32 C, row-major, C[i*n+j] = sum_p A[i*n+p] * B[p*n+j].
//
// The loop is fixed i -> j -> p and -ffp-contract=off keeps the products from
// being fused, so the emitted bytes are a function of the input alone. The
// build script compiles this file twice: scalar, and -msimd128 for the SIMD
// variant. The SIMD variant must produce the SAME bytes (the reference hashes
// are recorded per variant, and equality is never forced with the JS kernel).
//
// C is scalar on purpose: a wasm module cannot reach WebGPU, so the load-bearing
// ABI here is the byte contract, not the arithmetic.

#define CCGSC_IN_CAPACITY  (2u * 1024u * 1024u)
#define CCGSC_OUT_CAPACITY (4u * 1024u * 1024u)

/* The SIMD twin is selected at COMPILE TIME, never at run time: a module
   contains exactly one arithmetic implementation. */
#if defined(MATMUL_WASM_SIMD)
#include <wasm_simd128.h>
#define MATMUL_VARIANT "simd"
#else
#define MATMUL_VARIANT "scalar"
#endif

static unsigned char g_in[CCGSC_IN_CAPACITY];
static unsigned char g_out[CCGSC_OUT_CAPACITY];

__attribute__((export_name("ccgsc_alloc")))
unsigned ccgsc_alloc(unsigned len)
{
    return len <= CCGSC_IN_CAPACITY ? (unsigned)(unsigned long)&g_in[0] : 0u;
}

__attribute__((export_name("ccgsc_out")))
unsigned ccgsc_out(void)
{
    return (unsigned)(unsigned long)&g_out[0];
}

static float f32_at(const unsigned char *base, unsigned index)
{
    const unsigned char *p = base + index * 4u;
    unsigned bits = (unsigned)p[0] | ((unsigned)p[1] << 8) | ((unsigned)p[2] << 16) | ((unsigned)p[3] << 24);
    float value;
    __builtin_memcpy(&value, &bits, sizeof value);
    return value;
}

static void f32_put(unsigned char *base, unsigned index, float value)
{
    unsigned bits;
    __builtin_memcpy(&bits, &value, sizeof bits);
    unsigned char *p = base + index * 4u;
    p[0] = (unsigned char)(bits & 0xffu);
    p[1] = (unsigned char)((bits >> 8) & 0xffu);
    p[2] = (unsigned char)((bits >> 16) & 0xffu);
    p[3] = (unsigned char)((bits >> 24) & 0xffu);
}

static unsigned u32_at(const unsigned char *p)
{
    return (unsigned)p[0] | ((unsigned)p[1] << 8) | ((unsigned)p[2] << 16) | ((unsigned)p[3] << 24);
}

__attribute__((export_name("ccgsc_run")))
int ccgsc_run(unsigned in_ptr, unsigned in_len)
{
    const unsigned char *in = (const unsigned char *)(unsigned long)in_ptr;
    if (in_len < 4u) return -1;                       // no header
    const unsigned n = u32_at(in);
    if (n == 0u || n > 240u) return -2;               // outside the supported ladder
    const unsigned cells = n * n;
    if (in_len != 4u + cells * 8u) return -3;         // header + A + B exact
    if (cells * 4u > CCGSC_OUT_CAPACITY) return -4;   // output would overflow

    const unsigned char *a = in + 4u;
    const unsigned char *b = a + cells * 4u;
    unsigned char *c = &g_out[0];

    /* Fixed i -> j -> p order. Each product is rounded to f32 before it is
       accumulated, exactly like the JS kernel's Math.fround chain. */
    for (unsigned i = 0; i < n; ++i) {
#if defined(MATMUL_WASM_SIMD)
        /* Four adjacent columns at a time. Each lane accumulates in the SAME
           p order as the scalar loop, and the product is a single vector
           multiply (no FMA, since -ffp-contract=off), so the emitted bytes are
           identical to the scalar artifact. */
        unsigned j = 0;
        for (; j + 4u <= n; j += 4u) {
            v128_t sum = wasm_f32x4_splat(0.0f);
            for (unsigned p = 0; p < n; ++p) {
                const v128_t left = wasm_f32x4_splat(f32_at(a, i * n + p));
                v128_t right;
                __builtin_memcpy(&right, b + (p * n + j) * 4u, sizeof right);
                sum = wasm_f32x4_add(sum, wasm_f32x4_mul(left, right));
            }
            __builtin_memcpy(c + (i * n + j) * 4u, &sum, sizeof sum);
        }
        for (; j < n; ++j) {
            float sum = 0.0f;
            for (unsigned p = 0; p < n; ++p) {
                sum = sum + f32_at(a, i * n + p) * f32_at(b, p * n + j);
            }
            f32_put(c, i * n + j, sum);
        }
#else
        for (unsigned j = 0; j < n; ++j) {
            float sum = 0.0f;
            for (unsigned p = 0; p < n; ++p) {
                const float product = f32_at(a, i * n + p) * f32_at(b, p * n + j);
                sum = sum + product;
            }
            f32_put(c, i * n + j, sum);
        }
#endif
    }
    return (int)(cells * 4u);
}
