// W10c D-W10-8 synthetic multi-file workload - TEST LAYER ONLY, not shipped.
// What/why: proves the multi-file path end to end. The entry module is the
// workload; `table.bin` is its data companion. The runner imports this module
// exactly like any other workload and passes the verified companion bytes as
// `ctx.files['table.bin']`; nothing here fetches, hashes or knows the platform.
//
// ABI: run(input, ctx) -> { output: { n, bytes }, meta } (design section 4).
// Output: n*n little-endian f32, each cell = (row*n+col) * table[col].
//
// One file, one import, no DOM, no network: the same rules as a real workload.

const ENTRY_PATH = 'table.bin';

// readTable validates the companion exactly as a workload should: it is data
// the signed envelope pinned, and a missing or malformed table fails the task.
function readTable(ctx) {
  const files = ctx && ctx.files ? ctx.files : {};
  const raw = files[ENTRY_PATH];
  if (!raw) throw new Error('test_multifile_missing_table');
  const bytes = raw instanceof Uint8Array ? raw : new Uint8Array(raw);
  if (bytes.byteLength < 4) throw new Error('test_multifile_table_too_small');
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const columns = view.getUint32(0, true);
  if (columns === 0 || bytes.byteLength !== 4 + columns * 4) {
    throw new Error('test_multifile_table_invalid');
  }
  const scale = [];
  for (let i = 0; i < columns; i++) scale.push(view.getFloat32(4 + i * 4, true));
  return scale;
}

export async function run(input, ctx) {
  if (!input || input.kind !== 'test-multifile') throw new Error('invalid_multifile_kind');
  const n = input.n;
  if (!Number.isInteger(n) || n < 1 || n > 64) throw new Error('invalid_multifile_size');
  const scale = readTable(ctx);
  const cells = n * n;
  const values = new Float32Array(cells);
  for (let row = 0; row < n; row++) {
    for (let col = 0; col < n; col++) {
      values[row * n + col] = (row * n + col) * (scale[col % scale.length] || 1);
    }
  }
  return {
    output: { n, bytes: new Uint8Array(values.buffer) },
    meta: { files: ['entry.js', ENTRY_PATH] }
  };
}
