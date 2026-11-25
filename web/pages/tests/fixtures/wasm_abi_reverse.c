/* W10d fixture source for pages/tests/wasm_abi.test.mjs.
   Built by the lane (clang 22, --target=wasm32) and inlined as a byte array, so
   the platform test suite needs no C toolchain. Keep it as small as possible:
   reverse the input bytes into the output region. */

#define IN_REGION  1024u
#define OUT_REGION 2048u

static unsigned char g_in[256];

__attribute__((export_name("ccgsc_alloc")))
unsigned ccgsc_alloc(unsigned len)
{
    return len <= sizeof g_in ? IN_REGION : 0u;
}

__attribute__((export_name("ccgsc_out")))
unsigned ccgsc_out(void)
{
    return OUT_REGION;
}

__attribute__((export_name("ccgsc_run")))
int ccgsc_run(unsigned in_ptr, unsigned in_len)
{
    const unsigned char *src = (const unsigned char *)(unsigned long)in_ptr;
    unsigned char *dst = (unsigned char *)(unsigned long)OUT_REGION;
    for (unsigned i = 0; i < in_len; i++) dst[i] = src[in_len - 1u - i];
    return (int)in_len;
}
