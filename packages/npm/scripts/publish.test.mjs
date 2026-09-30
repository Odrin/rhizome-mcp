import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { publishPackage } from './publish.mjs';

const notFound = { ok: false, stdout: JSON.stringify({ error: { code: 'E404', summary: 'Not Found' } }), stderr: '' };
const failureCases = [
  ['E401', { ok: false, stdout: JSON.stringify({ error: { code: 'E401' } }), stderr: 'authentication failed' }],
  ['E503', { ok: false, stdout: JSON.stringify({ error: { code: 'E503' } }), stderr: 'service unavailable' }],
  ['network failure', { ok: false, stdout: '', stderr: 'ECONNRESET' }],
  ['ambiguous E404 text', { ok: false, stdout: '', stderr: 'proxy returned E404' }],
  ['malformed JSON', { ok: true, stdout: '{broken', stderr: '' }],
  ['empty response', { ok: true, stdout: '', stderr: '' }],
  ['error object on success', { ok: true, stdout: JSON.stringify({ error: { code: 'E404' } }), stderr: '' }],
  ['non-string version', { ok: true, stdout: '[42]', stderr: '' }],
  ['invalid version string', { ok: true, stdout: '["not-a-version"]', stderr: '' }],
];

function fixture(testContext, versionsResponse, versionResponse = notFound) {
  const root = mkdtempSync(path.join(tmpdir(), 'rhizome-publish-test-'));
  const packageDir = path.join(root, 'rhizome-mcp');
  mkdirSync(packageDir);
  writeFileSync(path.join(packageDir, 'package.json'), JSON.stringify({ name: 'rhizome-mcp' }));
  testContext.after(() => rmSync(root, { recursive: true, force: true }));
  const writes = [];
  const reads = [];
  return {
    root,
    writes,
    reads,
    commands: {
      capture(command, args) {
        assert.equal(command, 'npm');
        assert.equal(args[0], 'view');
        assert.equal(args.at(-1), '--json');
        reads.push(args);
        if (args[2] === 'version') return versionResponse;
        assert.deepEqual(args, ['view', 'rhizome-mcp', 'versions', '--json']);
        return versionsResponse;
      },
      publish(command, args, cwd) {
        assert.equal(command, 'npm');
        assert.equal(cwd, packageDir);
        writes.push(args);
        return true;
      },
    },
  };
}

for (const [name, response] of failureCases) {
  test(`version-list ${name} aborts without registry writes`, (testContext) => {
    const state = fixture(testContext, response);
    assert.throws(() => publishPackage(state.root, 'rhizome-mcp', '1.2.3-beta.1', true, {}, state.commands));
    assert.deepEqual(state.writes, []);
  });
}

for (const [name, response] of failureCases) {
  for (const isBeta of [false, true]) {
    test(`idempotency ${name} aborts ${isBeta ? 'beta' : 'stable'} without registry writes`, (testContext) => {
      const state = fixture(testContext, notFound, response);
      assert.throws(() => publishPackage(state.root, 'rhizome-mcp', '1.2.3', isBeta, { noLatestFollow: true }, state.commands));
      assert.deepEqual(state.writes, []);
    });
  }
}

for (const [name, response, expectedTag] of [
  ['package E404', notFound, 'latest'],
  ['empty version list', { ok: true, stdout: '[]' }, 'latest'],
  ['bootstrap and beta versions', { ok: true, stdout: '["0.0.1", "1.2.3-beta.1"]' }, 'latest'],
  ['single beta version', { ok: true, stdout: '"1.2.3-beta.1"' }, 'latest'],
  ['real stable version list', { ok: true, stdout: '["0.0.1", "1.0.0", "1.2.3-beta.1"]' }, 'beta'],
  ['single stable version', { ok: true, stdout: '"1.0.0"' }, 'beta'],
]) {
  test(`${name} retains intended beta dist-tag`, (testContext) => {
    const state = fixture(testContext, response);
    publishPackage(state.root, 'rhizome-mcp', '1.2.3-beta.2', true, {}, state.commands);
    assert.deepEqual(state.writes, [['publish', '--provenance', '--tag', expectedTag]]);
  });
}

test('stable first publication uses latest without a version-list lookup', (testContext) => {
  const state = fixture(testContext, notFound);
  publishPackage(state.root, 'rhizome-mcp', '1.2.3', false, {}, state.commands);
  assert.deepEqual(state.writes, [['publish', '--provenance', '--tag', 'latest']]);
  assert.equal(state.reads.length, 1);
});

test('forced beta publication does not follow latest', (testContext) => {
  const state = fixture(testContext, notFound);
  publishPackage(state.root, 'rhizome-mcp', '1.2.3-beta.1', true, { noLatestFollow: true, dryRun: true }, state.commands);
  assert.deepEqual(state.writes, [['publish', '--provenance', '--tag', 'beta', '--dry-run']]);
  assert.equal(state.reads.length, 1);
});

test('already published version skips publication and tag selection', (testContext) => {
  const state = fixture(testContext, notFound, { ok: true, stdout: '"1.2.3-beta.1"' });
  publishPackage(state.root, 'rhizome-mcp', '1.2.3-beta.1', true, {}, state.commands);
  assert.deepEqual(state.writes, []);
  assert.equal(state.reads.length, 1);
});
