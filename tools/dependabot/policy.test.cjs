'use strict';
const assert = require('node:assert/strict');
const { test } = require('node:test');
const { eligible, patchChanges } = require('./policy.cjs');
const { run } = require('./automerge.cjs');
const repository = 'nkiyohara/corresync';
const head = 'a'.repeat(40);
const base = 'b'.repeat(40);
const moduleName = 'go.yaml.in/yaml/v3';
const hash = 'h1:' + 'A'.repeat(43) + '=';
const mod = version => `module example.test/app\n\ngo 1.26.0\n\nrequire (\n\t${moduleName} ${version}\n)\n`;
const sum = version => `${moduleName} ${version} ${hash}\n${moduleName} ${version}/go.mod ${hash}\n`;
function fixture() {
  return {
    repository,
    pr: { number: 1, state: 'open', draft: false, changed_files: 2, user: { login: 'dependabot[bot]', type: 'Bot' },
      base: { ref: 'main', sha: base, repo: { full_name: repository } },
      head: { ref: 'dependabot/go_modules/patch', sha: head, repo: { full_name: repository } } },
    files: ['go.mod', 'go.sum'].map(filename => ({ filename, status: 'modified' })),
    beforeMod: mod('v3.0.5'), afterMod: mod('v3.0.6'), beforeSum: sum('v3.0.5'), afterSum: sum('v3.0.6'),
  };
}

test('accepts a stable allowlisted patch with matching checksum changes', () => {
  assert.equal(eligible(fixture()), true);
});

for (const [name, mutate] of [
  ['human PR', f => { f.pr.user.login = 'contributor'; }],
  ['spoofed bot type', f => { f.pr.user.type = 'User'; }],
  ['fork', f => { f.pr.head.repo.full_name = 'someone/corresync'; }],
  ['draft', f => { f.pr.draft = true; }],
  ['closed PR', f => { f.pr.state = 'closed'; }],
  ['other base', f => { f.pr.base.ref = 'release/0.8'; }],
  ['workflow changes', f => { f.files[1].filename = '.github/workflows/ci.yml'; }],
  ['truncated file list', f => { f.pr.changed_files = 101; }],
  ['renamed manifest', f => { f.files[0].status = 'renamed'; }],
  ['Go version change', f => { f.afterMod = f.afterMod.replace('go 1.26.0', 'go 1.27.0'); }],
  ['new directive', f => { f.afterMod += 'replace example.test/a => example.test/b v1.0.0\n'; }],
  ['minor update', f => { f.afterMod = mod('v3.1.0'); }],
  ['major update', f => { f.afterMod = mod('v4.0.0'); }],
  ['prerelease', f => { f.afterMod = mod('v3.0.6-rc.1'); }],
  ['downgrade', f => { f.afterMod = mod('v3.0.4'); }],
  ['v0 patch', f => { f.beforeMod = mod('v0.1.5'); f.afterMod = mod('v0.1.6'); }],
  ['no update', f => { f.afterMod = f.beforeMod; }],
  ['unknown or sensitive module', f => {
    f.beforeMod = f.beforeMod.replace(moduleName, 'github.com/sigstore/sigstore-go');
    f.afterMod = f.afterMod.replace(moduleName, 'github.com/sigstore/sigstore-go');
  }],
  ['indirect dependency bump', f => {
    f.beforeMod += '\tgolang.org/x/oauth2 v0.37.0 // indirect\n';
    f.afterMod += '\tgolang.org/x/oauth2 v0.37.1 // indirect\n';
  }],
  ['unrelated checksum addition', f => { f.afterSum += `example.test/new v1.0.0 ${hash}\n`; }],
  ['rewritten existing checksum', f => {
    f.afterSum += sum('v3.0.5').replaceAll(hash, 'h1:' + 'B'.repeat(43) + '=');
  }],
  ['duplicated checksum', f => { f.afterSum += sum('v3.0.6'); }],
]) {
  test(`rejects ${name}`, () => {
    const f = fixture(); mutate(f); assert.equal(eligible(f), false);
  });
}

test('mixed patch and minor group is not a patch update', () => {
  const a = '\tgithub.com/google/uuid v1.6.0\n\tgo.yaml.in/yaml/v3 v3.0.5\n';
  const b = '\tgithub.com/google/uuid v1.7.0\n\tgo.yaml.in/yaml/v3 v3.0.6\n';
  assert.equal(patchChanges(a, b), null);
});

function runnerFixture(overrides = {}) {
  const f = fixture();
  const writes = [];
  const event = { workflow_run: { workflow_id: 7, event: 'pull_request', conclusion: 'success',
    head_repository: { full_name: repository }, head_sha: head, pull_requests: [{ number: 1 }] } };
  const latest = { head_sha: head, status: 'completed', conclusion: 'success' };
  const api = async (method, path, body) => {
    if (method === 'PUT') { writes.push({ path, body }); return { merged: true, sha: 'c'.repeat(40) }; }
    if (path.endsWith('/actions/workflows/ci.yml')) return { id: 7 };
    if (path.endsWith('/pulls/1')) return f.pr;
    if (path.includes('/files?')) return f.files;
    if (path.includes('/runs?')) return { workflow_runs: [latest] };
    if (path.includes('/contents/')) {
      const isBase = path.endsWith(base);
      const text = path.includes('/go.mod?') ? (isBase ? f.beforeMod : f.afterMod) : (isBase ? f.beforeSum : f.afterSum);
      return { type: 'file', encoding: 'base64', size: text.length, content: Buffer.from(text).toString('base64') };
    }
    throw new Error('Unexpected API path');
  };
  return { f, writes, latest, options: { api, event, eventName: 'workflow_run', repository, dryRun: false, ...overrides } };
}

test('merges only inspected SHA after successful CI', async () => {
  const f = runnerFixture(); await run(f.options);
  assert.deepEqual(f.writes, [{ path: `repos/${repository}/pulls/1/merge`, body: { sha: head, merge_method: 'squash' } }]);
});

test('dry run never writes', async () => {
  const f = runnerFixture({ dryRun: true });
  assert.equal((await run(f.options))[0].status, 'eligible (dry run)');
  assert.equal(f.writes.length, 0);
});

for (const [name, mutate] of [
  ['stale workflow event', f => { f.options.event.workflow_run.head_sha = base; }],
  ['failed CI', f => { f.latest.conclusion = 'failure'; }],
  ['CI rerun in progress', f => { f.latest.status = 'in_progress'; }],
  ['another workflow named CI', f => { f.options.event.workflow_run.workflow_id = 8; }],
  ['fork workflow', f => { f.options.event.workflow_run.head_repository.full_name = 'someone/corresync'; }],
  ['ineligible patch', f => { f.f.afterMod = mod('v3.1.0'); }],
]) {
  test(`does not merge ${name}`, async () => {
    const f = runnerFixture(); mutate(f); await run(f.options); assert.equal(f.writes.length, 0);
  });
}

test('server rejection is not treated as a merge', async () => {
  const f = runnerFixture(); const read = f.options.api;
  f.options.api = async (method, ...rest) => method === 'PUT' ? { merged: false } : read(method, ...rest);
  await assert.rejects(run(f.options), /not merged/);
});

test('manual evaluation validates the PR number', async () => {
  const f = runnerFixture({ eventName: 'workflow_dispatch', event: { inputs: { pull_request: '../other' } } });
  await assert.rejects(run(f.options), /valid PR number/);
  assert.equal(f.writes.length, 0);
});
