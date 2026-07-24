'use strict';

// Verifies the launcher forwards SIGTERM/SIGINT it receives through to the
// spawned child. Uses a small fake "binary" (a shebang script standing in
// for the real Go binary) laid out as a real @rhizome-mcp/<platform>
// optional dependency in a scratch node_modules tree, so the launcher's own
// require.resolve-based lookup is exercised unmodified - only the resolved
// binary is fake.
//
// Shebang scripts aren't executable directly on win32, so this is skipped
// there; the pack/install smoke test still covers win32 exit-code
// passthrough via cmd shims where a real binary is present.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const MAIN_PKG_DIR = path.resolve(__dirname, '..');
const PLATFORM_KEY = `${process.platform}-${process.arch}`;

const FAKE_BINARY_SOURCE = `#!/usr/bin/env node
process.stdout.write('fake-binary-ready\\n');
function handle(signal) {
  process.stdout.write('fake-binary-received:' + signal + '\\n');
  process.exit(0);
}
process.on('SIGTERM', () => handle('SIGTERM'));
process.on('SIGINT', () => handle('SIGINT'));
setInterval(() => {}, 1000);
`;

function waitForLine(readable, predicate, timeoutMs) {
  return new Promise((resolve, reject) => {
    let buffer = '';
    const timer = setTimeout(() => {
      readable.off('data', onData);
      reject(new Error(`timed out waiting for expected output; got so far:\n${buffer}`));
    }, timeoutMs);
    function onData(chunk) {
      buffer += chunk.toString('utf8');
      if (predicate(buffer)) {
        clearTimeout(timer);
        readable.off('data', onData);
        resolve(buffer);
      }
    }
    readable.on('data', onData);
  });
}

test('launcher forwards SIGTERM to the child process', { skip: process.platform === 'win32' }, async () => {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), 'rhizome-mcp-signal-test-'));
  try {
    const mainDest = path.join(sandbox, 'node_modules', 'rhizome-mcp');
    fs.mkdirSync(mainDest, { recursive: true });
    fs.cpSync(path.join(MAIN_PKG_DIR, 'bin'), path.join(mainDest, 'bin'), { recursive: true });
    fs.cpSync(path.join(MAIN_PKG_DIR, 'lib'), path.join(mainDest, 'lib'), { recursive: true });
    fs.cpSync(path.join(MAIN_PKG_DIR, 'package.json'), path.join(mainDest, 'package.json'));

    const fakePlatformDest = path.join(sandbox, 'node_modules', '@rhizome-mcp', PLATFORM_KEY);
    fs.mkdirSync(path.join(fakePlatformDest, 'bin'), { recursive: true });
    fs.writeFileSync(path.join(fakePlatformDest, 'package.json'), JSON.stringify({ name: `@rhizome-mcp/${PLATFORM_KEY}`, version: '0.0.1' }));
    const fakeBinaryPath = path.join(fakePlatformDest, 'bin', 'rhizome-mcp');
    fs.writeFileSync(fakeBinaryPath, FAKE_BINARY_SOURCE, { mode: 0o755 });
    fs.chmodSync(fakeBinaryPath, 0o755);

    const launcherPath = path.join(mainDest, 'bin', 'launcher.js');
    const child = spawn(process.execPath, [launcherPath, 'serve'], { cwd: sandbox });

    let stdout = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk.toString('utf8');
    });

    await waitForLine(child.stdout, (buf) => buf.includes('fake-binary-ready'), 5000);

    const exitPromise = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));

    child.kill('SIGTERM');

    await exitPromise;

    assert.match(stdout, /fake-binary-received:SIGTERM/, 'expected the fake binary to have received SIGTERM');
  } finally {
    fs.rmSync(sandbox, { recursive: true, force: true });
  }
});
