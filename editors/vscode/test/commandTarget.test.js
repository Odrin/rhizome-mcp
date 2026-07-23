'use strict';

/**
 * Fast, dependency-free unit tests for src/commandTarget.ts.
 *
 * These exercise only the pure logic backing the `rhizome-mcp.init` and
 * `rhizome-mcp.showBoard` commands (folder-count selection, tracker path,
 * board temp-file path) — no real filesystem access, no real workspace
 * folders, and no `vscode` module involved, so this runs with plain
 * `node --test test/commandTarget.test.js` in well under a second and needs
 * no VS Code download. Mirrors ../src/mcpProvider.ts + ./mcpProvider.test.js.
 *
 * commandTarget.ts is required directly (with its real .ts extension):
 * Node's built-in TypeScript support (type stripping, stable since Node 22)
 * strips the type annotations at load time, so this plain CommonJS test
 * file can require that ESM-syntax module synchronously. No compile step
 * needed.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const {
  TRACKER_FILENAME,
  selectTargetFolder,
  trackerPathFor,
  generateBoardTempFilePath,
} = require('../src/commandTarget.ts');

// ---------------------------------------------------------------------------
// TRACKER_FILENAME
// ---------------------------------------------------------------------------

test('TRACKER_FILENAME matches the marker file written by `rhizome-mcp init`', () => {
  assert.equal(TRACKER_FILENAME, '.agent-tracker.json');
});

// ---------------------------------------------------------------------------
// selectTargetFolder
// ---------------------------------------------------------------------------

test('selectTargetFolder reports "none" for an empty folder list', () => {
  assert.deepEqual(selectTargetFolder([]), { kind: 'none' });
});

test('selectTargetFolder reports "direct" with the sole folder for a single-folder list', () => {
  const folder = { name: 'my-repo' };
  assert.deepEqual(selectTargetFolder([folder]), { kind: 'direct', folder });
});

test('selectTargetFolder reports "ambiguous" with all folders for a multi-folder list', () => {
  const a = { name: 'a' };
  const b = { name: 'b' };
  const c = { name: 'c' };
  assert.deepEqual(selectTargetFolder([a, b, c]), { kind: 'ambiguous', folders: [a, b, c] });
});

test('selectTargetFolder returns a fresh array for "ambiguous", not the original reference', () => {
  const folders = [{ name: 'a' }, { name: 'b' }];
  const result = selectTargetFolder(folders);
  assert.equal(result.kind, 'ambiguous');
  assert.notEqual(result.folders, folders);
  assert.deepEqual(result.folders, folders);
});

// ---------------------------------------------------------------------------
// trackerPathFor
// ---------------------------------------------------------------------------

test('trackerPathFor joins the folder root with the tracker filename', () => {
  assert.equal(trackerPathFor('/repo/root'), path.join('/repo/root', '.agent-tracker.json'));
});

test('trackerPathFor works with a relative-looking root too (pure path join, no fs access)', () => {
  assert.equal(trackerPathFor('some/dir'), path.join('some/dir', '.agent-tracker.json'));
});

// ---------------------------------------------------------------------------
// generateBoardTempFilePath
// ---------------------------------------------------------------------------

test('generateBoardTempFilePath joins the tmp dir with a name that embeds the given unique id', () => {
  const result = generateBoardTempFilePath('/tmp', 'abc-123');
  assert.equal(result, path.join('/tmp', 'rhizome-board-abc-123.html'));
});

test('generateBoardTempFilePath produces different paths for different unique ids (no collisions across invocations)', () => {
  const first = generateBoardTempFilePath('/tmp', 'id-one');
  const second = generateBoardTempFilePath('/tmp', 'id-two');
  assert.notEqual(first, second);
});
