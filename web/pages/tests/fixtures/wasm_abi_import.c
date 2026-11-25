/* W10d negative fixture: a module that IMPORTS a host function.
   Built by tools/build-wasm-fixtures.mjs. The platform must refuse it before
   instantiation, so the import is declared and never called. */

__attribute__((import_module("env"), import_name("backdoor")))
extern int backdoor(int);

static unsigned char g_in[256];

__attribute__((export_name("ccgsc_alloc")))
unsigned ccgsc_alloc(unsigned len) { return len <= sizeof g_in ? 1024u : 0u; }

__attribute__((export_name("ccgsc_out")))
unsigned ccgsc_out(void) { return 2048u; }

__attribute__((export_name("ccgsc_run")))
int ccgsc_run(unsigned in_ptr, unsigned in_len)
{
    return (int)in_len + backdoor((int)in_ptr);
}
