'use strict';

/**
 * Fast, dependency-free unit tests for src/binaryResolver.ts.
 *
 * These exercise only the pure, dependency-injected logic (fs/spawn are
 * faked in-memory below) — no real process spawning, no real filesystem
 * access, and no `vscode` module involved, so this runs with plain
 * `node --test test/binaryResolver.test.js` in well under a second and
 * needs no VS Code download. Mirrors the pattern used by the sibling
 * ../../packages/npm/rhizome-mcp/lib/platform.js + test/platform.test.js.
 *
 * binaryResolver.ts is required directly (with its real .ts extension):
 * Node's built-in TypeScript support (type stripping, stable since Node 22)
 * strips the type annotations at load time, and its "require(esm)" support
 * lets this plain CommonJS test file require that ESM-syntax module
 * synchronously. No compile step needed.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const {
  getBundledBinaryName,
  getBundledBinaryPath,
  isExecutableMode,
  resolveBinary,
  parseVersionOutput,
  getBinaryVersion,
  checkVersionMismatch,
  resolveAndValidateBinary,
} = require('../src/binaryResolver.ts');

const EXEC_MODE = 0o755;
const NON_EXEC_MODE = 0o644;

/**
 * Builds a fake fs.* implementation over an in-memory map of
 * path -> { mode, isFile, statThrows }. Records every chmodSync call.
 */
function makeFakeFs(entries = {}) {
  const files = new Map(Object.entries(entries));
  const chmodCalls = [];

  return {
    chmodCalls,
    existsSync(p) {
      return files.has(p);
    },
    statSync(p) {
      const entry = files.get(p);
      if (!entry) {
        throw Object.assign(new Error(`ENOENT: no such file or directory, stat '${p}'`), { code: 'ENOENT' });
      }
      if (entry.statThrows) {
        throw entry.statThrows;
      }
      const mode = entry.mode ?? EXEC_MODE;
      const isFile = entry.isFile ?? true;
      return { mode, isFile: () => isFile };
    },
    chmodSync(p, mode) {
      chmodCalls.push({ path: p, mode });
    },
  };
}

function makeLogger() {
  const infos = [];
  const warns = [];
  return {
    infos,
    warns,
    info: (msg) => infos.push(msg),
    warn: (msg) => warns.push(msg),
  };
}

// ---------------------------------------------------------------------------
// getBundledBinaryName / getBundledBinaryPath
// ---------------------------------------------------------------------------

test('getBundledBinaryName appends .exe only on win32', () => {
  assert.equal(getBundledBinaryName('win32'), 'rhizome-mcp.exe');
  assert.equal(getBundledBinaryName('darwin'), 'rhizome-mcp');
  assert.equal(getBundledBinaryName('linux'), 'rhizome-mcp');
});

test('getBundledBinaryPath joins extensionPath/bin/<name> using platform-correct separators', () => {
  assert.equal(getBundledBinaryPath('/ext', 'linux'), '/ext/bin/rhizome-mcp');
  assert.equal(getBundledBinaryPath('/ext', 'darwin'), '/ext/bin/rhizome-mcp');
  assert.equal(getBundledBinaryPath('C:\\ext', 'win32'), 'C:\\ext\\bin\\rhizome-mcp.exe');
});

// ---------------------------------------------------------------------------
// isExecutableMode
// ---------------------------------------------------------------------------

test('isExecutableMode detects any of the exec bits', () => {
  assert.equal(isExecutableMode(0o755), true);
  assert.equal(isExecutableMode(0o111), true);
  assert.equal(isExecutableMode(0o100), true); // owner-exec only
  assert.equal(isExecutableMode(0o644), false);
  assert.equal(isExecutableMode(0o000), false);
});

// ---------------------------------------------------------------------------
// resolveBinary: precedence / resolution order
// ---------------------------------------------------------------------------

test('resolveBinary prefers a valid rhizome.serverPath override over a bundled binary', () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const fakeFs = makeFakeFs({
    '/custom/rhizome-mcp': { mode: EXEC_MODE },
    [bundledPath]: { mode: EXEC_MODE },
  });

  const result = resolveBinary('/custom/rhizome-mcp', {
    platform: 'linux',
    env: {},
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.deepEqual(result, { ok: true, source: 'override', binaryPath: '/custom/rhizome-mcp' });
  // Precedence means the bundled path is never even chmod'd/touched.
  assert.equal(fakeFs.chmodCalls.length, 0);
});

test('resolveBinary falls back to the bundled binary when no override is configured', () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const fakeFs = makeFakeFs({
    [bundledPath]: { mode: NON_EXEC_MODE }, // deliberately non-executable before chmod
  });

  const result = resolveBinary(undefined, {
    platform: 'linux',
    env: { PATH: '/usr/bin' },
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.deepEqual(result, { ok: true, source: 'bundled', binaryPath: bundledPath });
});

test('resolveBinary treats an empty-string override the same as unset (falls through)', () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const fakeFs = makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } });

  const result = resolveBinary('   ', {
    platform: 'linux',
    env: {},
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.deepEqual(result, { ok: true, source: 'bundled', binaryPath: bundledPath });
});

test('resolveBinary falls back to PATH when there is no override and no bundled binary', () => {
  const fakeFs = makeFakeFs({
    '/usr/local/bin/rhizome-mcp': { mode: EXEC_MODE },
  });

  const result = resolveBinary(undefined, {
    platform: 'linux',
    // path.posix.delimiter (always ':'), not the native path.delimiter —
    // this simulates 'linux' regardless of the host OS actually running
    // the test (matters on Windows, where path.delimiter is ';').
    env: { PATH: `/usr/bin${path.posix.delimiter}/usr/local/bin` },
    extensionPath: '/ext', // bundled binary does not exist under this path
    fs: fakeFs,
  });

  assert.deepEqual(result, {
    ok: true,
    source: 'path',
    binaryPath: '/usr/local/bin/rhizome-mcp',
  });
});

test('resolveBinary searches PATH directories in order and returns the first match', () => {
  const fakeFs = makeFakeFs({
    '/second/rhizome-mcp': { mode: EXEC_MODE },
    '/third/rhizome-mcp': { mode: EXEC_MODE },
  });

  const result = resolveBinary(undefined, {
    platform: 'linux',
    // path.posix.delimiter, same reasoning as the test above.
    env: { PATH: ['/first', '/second', '/third'].join(path.posix.delimiter) },
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.equal(result.ok, true);
  assert.equal(result.binaryPath, '/second/rhizome-mcp');
});

test('resolveBinary on win32 checks for rhizome-mcp.exe on PATH before the extension-less name', () => {
  const fakeFs = makeFakeFs({
    'C:\\tools\\rhizome-mcp.exe': { mode: EXEC_MODE },
    'C:\\tools\\rhizome-mcp': { mode: EXEC_MODE },
  });

  const result = resolveBinary(undefined, {
    platform: 'win32',
    env: { Path: 'C:\\tools' },
    extensionPath: 'C:\\ext',
    fs: fakeFs,
  });

  assert.deepEqual(result, {
    ok: true,
    source: 'path',
    binaryPath: 'C:\\tools\\rhizome-mcp.exe',
  });
});

test('resolveBinary fails with not-found when nothing resolves', () => {
  const fakeFs = makeFakeFs({});

  const result = resolveBinary(undefined, {
    platform: 'linux',
    env: { PATH: '/usr/bin' },
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.equal(result.ok, false);
  assert.equal(result.reason, 'not-found');
  assert.match(result.message, /could not locate/i);
});

// ---------------------------------------------------------------------------
// resolveBinary: override validation (never silently falls through)
// ---------------------------------------------------------------------------

test('resolveBinary reports invalid-override when the configured path does not exist, and does NOT fall through to a valid bundled binary', () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const fakeFs = makeFakeFs({
    [bundledPath]: { mode: EXEC_MODE }, // would resolve fine if we fell through
  });

  const result = resolveBinary('/does/not/exist', {
    platform: 'linux',
    env: {},
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.equal(result.ok, false);
  assert.equal(result.reason, 'invalid-override');
  assert.match(result.message, /does not exist/);
});

test('resolveBinary reports invalid-override when the configured path is not executable on unix', () => {
  const fakeFs = makeFakeFs({
    '/custom/rhizome-mcp': { mode: NON_EXEC_MODE },
  });

  const result = resolveBinary('/custom/rhizome-mcp', {
    platform: 'darwin',
    env: {},
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.equal(result.ok, false);
  assert.equal(result.reason, 'invalid-override');
  assert.match(result.message, /not executable/);
});

test('resolveBinary only checks existence (not the exec bit) for an override on win32', () => {
  const fakeFs = makeFakeFs({
    'C:\\custom\\rhizome-mcp.exe': { mode: 0o000 }, // no exec bits at all
  });

  const result = resolveBinary('C:\\custom\\rhizome-mcp.exe', {
    platform: 'win32',
    env: {},
    extensionPath: 'C:\\ext',
    fs: fakeFs,
  });

  assert.deepEqual(result, {
    ok: true,
    source: 'override',
    binaryPath: 'C:\\custom\\rhizome-mcp.exe',
  });
});

test('resolveBinary reports invalid-override when the configured path is a directory', () => {
  const fakeFs = makeFakeFs({
    '/custom/some-dir': { mode: EXEC_MODE, isFile: false },
  });

  const result = resolveBinary('/custom/some-dir', {
    platform: 'linux',
    env: {},
    extensionPath: '/ext',
    fs: fakeFs,
  });

  assert.equal(result.ok, false);
  assert.equal(result.reason, 'invalid-override');
  assert.match(result.message, /not a file/);
});

// ---------------------------------------------------------------------------
// resolveBinary: chmod on the bundled binary
// ---------------------------------------------------------------------------

test('resolveBinary chmods the bundled binary to 0o755 on unix every time it resolves there', () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const fakeFs = makeFakeFs({ [bundledPath]: { mode: NON_EXEC_MODE } });
  const deps = { platform: 'linux', env: {}, extensionPath: '/ext', fs: fakeFs };

  resolveBinary(undefined, deps);
  resolveBinary(undefined, deps);

  assert.deepEqual(fakeFs.chmodCalls, [
    { path: bundledPath, mode: 0o755 },
    { path: bundledPath, mode: 0o755 },
  ]);
});

test('resolveBinary does NOT chmod on win32', () => {
  const bundledPath = getBundledBinaryPath('C:\\ext', 'win32');
  const fakeFs = makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } });

  resolveBinary(undefined, { platform: 'win32', env: {}, extensionPath: 'C:\\ext', fs: fakeFs });

  assert.equal(fakeFs.chmodCalls.length, 0);
});

test('resolveBinary does NOT chmod when resolving to an override or a PATH match', () => {
  const overrideFs = makeFakeFs({ '/custom/rhizome-mcp': { mode: EXEC_MODE } });
  resolveBinary('/custom/rhizome-mcp', { platform: 'linux', env: {}, extensionPath: '/ext', fs: overrideFs });
  assert.equal(overrideFs.chmodCalls.length, 0);

  const pathFs = makeFakeFs({ '/usr/bin/rhizome-mcp': { mode: EXEC_MODE } });
  resolveBinary(undefined, { platform: 'linux', env: { PATH: '/usr/bin' }, extensionPath: '/ext', fs: pathFs });
  assert.equal(pathFs.chmodCalls.length, 0);
});

// ---------------------------------------------------------------------------
// parseVersionOutput
// ---------------------------------------------------------------------------

test('parseVersionOutput parses the exact main.go output format', () => {
  const output = 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)';
  assert.equal(parseVersionOutput(output), 'v1.0.0-beta.2');
});

test('parseVersionOutput tolerates a trailing newline', () => {
  const output = 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)\n';
  assert.equal(parseVersionOutput(output), 'v1.0.0-beta.2');
});

test('parseVersionOutput parses a plain semver too', () => {
  const output = 'rhizome-mcp v1.2.3 (commit abcdef0, built 2026-01-01T00:00:00Z)';
  assert.equal(parseVersionOutput(output), 'v1.2.3');
});

test('parseVersionOutput returns null for malformed/unexpected output', () => {
  assert.equal(parseVersionOutput(''), null);
  assert.equal(parseVersionOutput('not the expected format'), null);
  assert.equal(parseVersionOutput('rhizome-mcp\n'), null);
  assert.equal(parseVersionOutput('command not found: rhizome-mcp'), null);
});

// ---------------------------------------------------------------------------
// getBinaryVersion: spawning + caching
// ---------------------------------------------------------------------------

test('getBinaryVersion spawns --version and parses the result', async () => {
  const calls = [];
  const spawnFn = async (binaryPath, args) => {
    calls.push({ binaryPath, args });
    return { stdout: 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)', code: 0 };
  };

  const cache = new Map();
  const info = await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);

  assert.equal(info.version, 'v1.0.0-beta.2');
  assert.deepEqual(calls, [{ binaryPath: '/bin/rhizome-mcp', args: ['--version'] }]);
});

test('getBinaryVersion caches the result and does not re-spawn for the same path', async () => {
  let callCount = 0;
  const spawnFn = async () => {
    callCount += 1;
    return { stdout: 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)', code: 0 };
  };

  const cache = new Map();
  await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);
  await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);
  await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);

  assert.equal(callCount, 1);
});

test('getBinaryVersion treats a malformed/unexpected --version output as unknown, not a crash', async () => {
  const spawnFn = async () => ({ stdout: 'garbage output', code: 0 });
  const cache = new Map();

  const info = await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);

  assert.equal(info.version, null);
  assert.equal(info.raw, 'garbage output');
});

test('getBinaryVersion treats a spawn failure as unknown version rather than throwing', async () => {
  const spawnFn = async () => {
    throw new Error('ENOENT');
  };
  const cache = new Map();

  const info = await getBinaryVersion('/bin/rhizome-mcp', spawnFn, cache);

  assert.equal(info.version, null);
});

// ---------------------------------------------------------------------------
// checkVersionMismatch
// ---------------------------------------------------------------------------

test('checkVersionMismatch never warns for a bundled binary, even with a different version', () => {
  assert.equal(checkVersionMismatch('bundled', 'v1.0.0-beta.2', '0.0.1'), null);
});

test('checkVersionMismatch is silent when versions match', () => {
  assert.equal(checkVersionMismatch('override', '0.0.1', '0.0.1'), null);
  assert.equal(checkVersionMismatch('path', '0.0.1', '0.0.1'), null);
});

test('checkVersionMismatch warns for override/path sources with a differing version', () => {
  const overrideMsg = checkVersionMismatch('override', 'v1.0.0-beta.2', '0.0.1');
  assert.match(overrideMsg, /v1\.0\.0-beta\.2/);
  assert.match(overrideMsg, /0\.0\.1/);

  const pathMsg = checkVersionMismatch('path', 'v1.0.0-beta.2', '0.0.1');
  assert.match(pathMsg, /v1\.0\.0-beta\.2/);
});

// ---------------------------------------------------------------------------
// resolveAndValidateBinary: end-to-end orchestration
// ---------------------------------------------------------------------------

function baseAndValidateDeps(overrides = {}) {
  return {
    platform: 'linux',
    env: {},
    extensionPath: '/ext',
    extensionVersion: '0.0.1',
    fs: makeFakeFs({}),
    spawnFn: async () => ({ stdout: 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)', code: 0 }),
    logger: makeLogger(),
    versionCache: new Map(),
    ...overrides,
  };
}

test('resolveAndValidateBinary returns the resolved path, source, and version on success', async () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const deps = baseAndValidateDeps({ fs: makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } }) });

  const result = await resolveAndValidateBinary(undefined, deps);

  assert.equal(result.failure, null);
  assert.equal(result.binaryPath, bundledPath);
  assert.equal(result.source, 'bundled');
  assert.equal(result.version, 'v1.0.0-beta.2');
});

test('resolveAndValidateBinary: nothing resolves -> failure is populated and a warning is logged, path/version are null', async () => {
  const deps = baseAndValidateDeps(); // empty fs, no override, no PATH

  const result = await resolveAndValidateBinary(undefined, deps);

  assert.equal(result.binaryPath, null);
  assert.equal(result.source, null);
  assert.equal(result.version, null);
  assert.ok(result.failure);
  assert.equal(result.failure.reason, 'not-found');
  assert.equal(deps.logger.warns.length, 1);
});

test('resolveAndValidateBinary: invalid override is a failure and never falls through to a valid bundled binary', async () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const deps = baseAndValidateDeps({ fs: makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } }) });

  const result = await resolveAndValidateBinary('/does/not/exist', deps);

  assert.equal(result.binaryPath, null);
  assert.equal(result.failure.reason, 'invalid-override');
});

test('resolveAndValidateBinary: malformed version output does not fail resolution, but logs a warning', async () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const deps = baseAndValidateDeps({
    fs: makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } }),
    spawnFn: async () => ({ stdout: 'unexpected garbage', code: 0 }),
  });

  const result = await resolveAndValidateBinary(undefined, deps);

  assert.equal(result.failure, null);
  assert.equal(result.binaryPath, bundledPath, 'the resolved binary path must still be usable');
  assert.equal(result.version, null);
  assert.ok(deps.logger.warns.some((w) => /version/i.test(w)));
});

test('resolveAndValidateBinary: warns on version mismatch for a PATH-resolved binary', async () => {
  const deps = baseAndValidateDeps({
    fs: makeFakeFs({ '/usr/bin/rhizome-mcp': { mode: EXEC_MODE } }),
    env: { PATH: '/usr/bin' },
    extensionVersion: '0.0.1',
  });

  const result = await resolveAndValidateBinary(undefined, deps);

  assert.equal(result.failure, null);
  assert.equal(result.version, 'v1.0.0-beta.2');
  assert.ok(deps.logger.warns.some((w) => w.includes('v1.0.0-beta.2') && w.includes('0.0.1')));
});

test('resolveAndValidateBinary: does not warn on version mismatch for the bundled binary', async () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  const deps = baseAndValidateDeps({
    fs: makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } }),
    extensionVersion: '0.0.1', // differs from the v1.0.0-beta.2 the fake spawnFn reports
  });

  const result = await resolveAndValidateBinary(undefined, deps);

  assert.equal(result.failure, null);
  assert.deepEqual(deps.logger.warns, []);
});

test('resolveAndValidateBinary only spawns --version once across repeated calls sharing a versionCache', async () => {
  const bundledPath = getBundledBinaryPath('/ext', 'linux');
  let spawnCount = 0;
  const sharedCache = new Map();
  const makeDeps = () =>
    baseAndValidateDeps({
      fs: makeFakeFs({ [bundledPath]: { mode: EXEC_MODE } }),
      versionCache: sharedCache,
      spawnFn: async () => {
        spawnCount += 1;
        return { stdout: 'rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)', code: 0 };
      },
    });

  await resolveAndValidateBinary(undefined, makeDeps());
  await resolveAndValidateBinary(undefined, makeDeps());

  assert.equal(spawnCount, 1);
});
