'use strict';

/**
 * Fast, dependency-free unit tests for src/mcpProvider.ts.
 *
 * These exercise only the pure logic (label computation, and the
 * `.vscode/mcp.json` duplicate-detection rule) — no real filesystem access,
 * no real workspace folders, and no `vscode` module involved, so this runs
 * with plain `node --test test/mcpProvider.test.js` in well under a second
 * and needs no VS Code download. Mirrors ../src/binaryResolver.ts +
 * ./binaryResolver.test.js.
 *
 * mcpProvider.ts is required directly (with its real .ts extension): Node's
 * built-in TypeScript support (type stripping, stable since Node 22) strips
 * the type annotations at load time, so this plain CommonJS test file can
 * require that ESM-syntax module synchronously. No compile step needed.
 */

const test = require('node:test');
const assert = require('node:assert/strict');

const { computeServerLabel, hasRhizomeServerEntry, TRACKER_FILENAME } = require('../src/mcpProvider.ts');

// ---------------------------------------------------------------------------
// TRACKER_FILENAME
// ---------------------------------------------------------------------------

test('TRACKER_FILENAME is the marker file written by `rhizome-mcp init`', () => {
  assert.equal(TRACKER_FILENAME, '.agent-tracker.json');
});

// ---------------------------------------------------------------------------
// computeServerLabel
// ---------------------------------------------------------------------------

test('computeServerLabel returns the bare "rhizome" label for a single-folder workspace', () => {
  assert.equal(computeServerLabel('my-repo', 1), 'rhizome');
});

test('computeServerLabel qualifies the label with the folder name once there is more than one folder', () => {
  assert.equal(computeServerLabel('my-repo', 2), 'rhizome (my-repo)');
  assert.equal(computeServerLabel('other-repo', 3), 'rhizome (other-repo)');
});

test('computeServerLabel uses the total folder count, not a per-folder flag', () => {
  // Even a folder named oddly should just be interpolated verbatim once multi-root.
  assert.equal(computeServerLabel('folder (2)', 2), 'rhizome (folder (2))');
});

// ---------------------------------------------------------------------------
// hasRhizomeServerEntry
// ---------------------------------------------------------------------------

test('hasRhizomeServerEntry is true when servers.rhizome-mcp exists', () => {
  const raw = JSON.stringify({
    servers: {
      'rhizome-mcp': { type: 'stdio', command: '/path/to/rhizome-mcp', args: ['serve'] },
    },
  });
  assert.equal(hasRhizomeServerEntry(raw), true);
});

test('hasRhizomeServerEntry is true even if rhizome-mcp is not the only server', () => {
  const raw = JSON.stringify({
    servers: {
      'some-other-server': { type: 'stdio', command: 'foo' },
      'rhizome-mcp': { type: 'stdio', command: '/path/to/rhizome-mcp', args: ['serve'] },
    },
  });
  assert.equal(hasRhizomeServerEntry(raw), true);
});

test('hasRhizomeServerEntry is false when servers exists but has no rhizome-mcp key', () => {
  const raw = JSON.stringify({
    servers: {
      'some-other-server': { type: 'stdio', command: 'foo' },
    },
  });
  assert.equal(hasRhizomeServerEntry(raw), false);
});

test('hasRhizomeServerEntry is false when there is no servers key at all', () => {
  assert.equal(hasRhizomeServerEntry(JSON.stringify({})), false);
  assert.equal(hasRhizomeServerEntry(JSON.stringify({ other: 1 })), false);
});

test('hasRhizomeServerEntry is false when servers is not an object', () => {
  assert.equal(hasRhizomeServerEntry(JSON.stringify({ servers: 'nope' })), false);
  assert.equal(hasRhizomeServerEntry(JSON.stringify({ servers: null })), false);
  assert.equal(hasRhizomeServerEntry(JSON.stringify({ servers: ['rhizome-mcp'] })), false);
});

test('hasRhizomeServerEntry is false (not thrown) for malformed / unparseable JSON', () => {
  assert.equal(hasRhizomeServerEntry('{ this is not json'), false);
  assert.equal(hasRhizomeServerEntry(''), false);
  assert.equal(hasRhizomeServerEntry('null'), false);
  assert.equal(hasRhizomeServerEntry('[]'), false);
  assert.equal(hasRhizomeServerEntry('"just a string"'), false);
  assert.equal(hasRhizomeServerEntry('42'), false);
});
