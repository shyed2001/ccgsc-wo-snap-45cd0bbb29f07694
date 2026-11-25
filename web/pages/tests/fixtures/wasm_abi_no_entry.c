/* W10d negative fixture: a well-formed module that does NOT export the ABI.
   Built by tools/build-wasm-fixtures.mjs and used to prove the classifier
   declines it instead of running something with no entry. */

static unsigned char g_in[64];

__attribute__((export_name("only_this")))
int only_this(unsigned x) { return (int)x + (int)g_in[0]; }
