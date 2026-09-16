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
const STARTUP_TIMEOUT_MS = 15000;
const EXIT_TIMEOUT_MS = 5000;

const FAKE_BINARY_SOURCE = `#!/usr/bin/env node
const fs = require('node:fs');
function handle(signal) {
  fs.writeFileSync(process.env.RHIZOME_MCP_SIGNAL_MARKER, signal);
  process.exit(0);
}
process.on('SIGTERM', () => handle('SIGTERM'));
process.on('SIGINT', () => handle('SIGINT'));
process.stdout.write('fake-binary-ready\\n');
setInterval(() => {}, 1000);
`;

function waitForLine(child, predicate, timeoutMs) {
  return new Promise((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error(
        `timed out waiting for expected output; stdout:\n${stdout}\nstderr:\n${stderr}`
      ));
    }, timeoutMs);
    function cleanup() {
      clearTimeout(timer);
      child.stdout.off('data', onStdout);
      child.stderr.off('data', onStderr);
      child.off('close', onClose);
    }
    function onStdout(chunk) {
      stdout += chunk.toString('utf8');
      if (predicate(stdout)) {
        cleanup();
        resolve(stdout);
      }
    }
    function onStderr(chunk) {
      stderr += chunk.toString('utf8');
    }
    function onClose(code, signal) {
      cleanup();
      reject(new Error(
        `launcher exited before readiness (code=${code}, signal=${signal}); stdout:\n${stdout}\nstderr:\n${stderr}`
      ));
    }
    child.stdout.on('data', onStdout);
    child.stderr.on('data', onStderr);
    child.once('close', onClose);
  });
}

function waitForClose(child, timeoutMs) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      child.off('close', onClose);
      reject(new Error('timed out waiting for launcher to exit'));
    }, timeoutMs);
    function onClose(code, signal) {
      clearTimeout(timer);
      resolve({ code, signal });
    }
    child.once('close', onClose);
  });
}

test('launcher forwards SIGTERM to the child process', { skip: process.platform === 'win32' }, async () => {
  const sandbox = fs.mkdtempSync(path.join(os.tmpdir(), 'rhizome-mcp-signal-test-'));
  let child;
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
    const signalMarkerPath = path.join(sandbox, 'received-signal');
    child = spawn(process.execPath, [launcherPath, 'serve'], {
      cwd: sandbox,
      env: { ...process.env, RHIZOME_MCP_SIGNAL_MARKER: signalMarkerPath },
    });

    await waitForLine(child, (buf) => buf.includes('fake-binary-ready'), STARTUP_TIMEOUT_MS);
    const closePromise = waitForClose(child, EXIT_TIMEOUT_MS);
    assert.equal(child.kill('SIGTERM'), true, 'expected launcher to accept SIGTERM');
    await closePromise;

    assert.equal(fs.readFileSync(signalMarkerPath, 'utf8'), 'SIGTERM');
  } finally {
    try {
      if (child && child.exitCode === null && child.signalCode === null) {
        const closePromise = waitForClose(child, EXIT_TIMEOUT_MS);
        child.kill('SIGKILL');
        await closePromise;
      }
    } finally {
      fs.rmSync(sandbox, { recursive: true, force: true });
    }
  }
});
