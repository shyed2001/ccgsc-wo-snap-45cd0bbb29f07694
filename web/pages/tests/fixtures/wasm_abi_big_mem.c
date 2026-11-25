/* W10d negative fixture: a well-formed ABI module that allocates a large
   static buffer, so its linear memory is several pages. Built by
   tools/build-wasm-fixtures.mjs; used to prove the page cap is enforced on the
   REAL memory before the run, not merely parsed and ignored. */

#define BIG_BYTES (8u * 65536u)

static unsigned char g_big[BIG_BYTES];
static unsigned char g_out[16];

__attribute__((export_name("ccgsc_alloc")))
unsigned ccgsc_alloc(unsigned len) { return len <= 16u ? 1024u : 0u; }

__attribute__((export_name("ccgsc_out")))
unsigned ccgsc_out(void) { return (unsigned)(unsigned long)&g_out[0]; }

__attribute__((export_name("ccgsc_run")))
int ccgsc_run(unsigned in_ptr, unsigned in_len)
{
    /* Touch the large buffer so the linker cannot drop it. */
    g_big[0] = g_big[0] + (unsigned char)in_len;
    (void)in_ptr;
    return (int)in_len;
}
