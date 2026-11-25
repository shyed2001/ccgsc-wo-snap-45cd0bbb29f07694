// W10 CR-53 build gate: verify the independently stored reference workload.
// This script no longer copies/inlines workload source into platform JavaScript.
// Usage: node web/pages/js/build-demo-workload.mjs [--check]
// Both modes verify only; the workload is fetched as a separate artifact at runtime.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const workloadRoot = path.join(here, '..', '..', 'workloads');
const dirs = fs.readdirSync(workloadRoot, { withFileTypes: true }).filter(e => e.isDirectory());
const demo = dirs.map(e => path.join(workloadRoot, e.name)).find(dir => {
  try {
    const m = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
    // Only a manifest with a demo request discriminator qualifies. Independent
    // workload modules may expose a demoInput without being the platform mock.
    return typeof m.demoInput?.json?.kind === 'string' && m.demoInput.json.kind.length > 0;
  } catch { return false; }
});
if (!demo) throw new Error('mock_artifact_manifest_missing');
const manifest = JSON.parse(fs.readFileSync(path.join(demo, 'manifest.json'), 'utf8'));
if (!manifest.entry || !manifest.sha256 || !manifest.demoInput) throw new Error('mock_artifact_manifest_invalid');
const source = fs.readFileSync(path.join(demo, manifest.entry));
const actual = crypto.createHash('sha256').update(source).digest('hex');
if (actual !== manifest.sha256) throw new Error('mock_artifact_digest_mismatch');
if (!manifest.demoInput.json || !manifest.demoInput.json.kind) throw new Error('mock_artifact_demo_input_invalid');
const generated = path.join(here, 'demo_workload.js');
if (fs.existsSync(generated)) throw new Error('CR-53: generated workload remains in platform JS directory');
console.log('mock workload artifact manifest pin verified; platform has no inlined demo workload');
