'use strict';

// End-to-end smoke test: pack the main package and the platform package that
// matches the machine actually running this test, install both tarballs into
// a scratch npm prefix (as npm would for a real end user), then confirm the
// installed `rhizome-mcp` launcher behaves exactly like the raw Go binary it
// wraps: identical --version stdout, identical (zero) exit code, and
// identical (non-zero) exit code for a bad invocation.
//
// This only exercises the platform pair that has a *real* binary checked
// into this repo checkout (see packages/npm/README.md) - other platforms
// are populated by CI at release time and are not smoke-testable here.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const PACKAGES_NPM_DIR = path.resolve(__dirname, '..', '..');
const MAIN_PKG_DIR = path.join(PACKAGES_NPM_DIR, 'rhizome-mcp');
const PLATFORM_DIR_NAME = `${process.platform}-${process.arch}`;
const PLATFORM_PKG_DIR = path.join(PACKAGES_NPM_DIR, PLATFORM_DIR_NAME);
const BINARY_REL_PATH = process.platform === 'win32' ? 'bin/rhizome-mcp.exe' : 'bin/rhizome-mcp';
const REAL_BINARY_PATH = path.join(PLATFORM_PKG_DIR, BINARY_REL_PATH);

const NPM_CMD = process.platform === 'win32' ? 'npm.cmd' : 'npm';

function npmPack(pkgDir, destDir) {
  const result = spawnSync(NPM_CMD, ['pack', pkgDir, '--pack-destination', destDir, '--json'], {
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, `npm pack failed for ${pkgDir}:\n${result.stderr}`);
  const parsed = JSON.parse(result.stdout);
  // npm's `pack --json` output shape has varied across versions: some emit
  // an array of pack results, others (e.g. npm 12) emit an object keyed by
  // package name. Handle both.
  const entry = Array.isArray(parsed) ? parsed[0] : Object.values(parsed)[0];
  return path.join(destDir, entry.filename);
}

function npmInstall(prefixDir, tarballs) {
  const result = spawnSync(
    NPM_CMD,
    ['install', '--prefix', prefixDir, '--no-save', '--no-optional', '--no-audit', '--no-fund', ...tarballs],
    { encoding: 'utf8' }
  );
  assert.equal(result.status, 0, `npm install failed:\n${result.stderr}`);
}

test('npm pack + install smoke test: installed launcher matches the direct binary', (t) => {
  if (!fs.existsSync(REAL_BINARY_PATH)) {
    t.skip(
      `no real binary checked in for ${PLATFORM_DIR_NAME} in this checkout ` +
        `(expected at ${REAL_BINARY_PATH}); see packages/npm/README.md`
    );
    return;
  }

  const workDir = fs.mkdtempSync(path.join(os.tmpdir(), 'rhizome-mcp-npm-smoke-'));
  const packDir = path.join(workDir, 'pack');
  const installDir = path.join(workDir, 'install');
  fs.mkdirSync(packDir, { recursive: true });
  fs.mkdirSync(installDir, { recursive: true });

  try {
    const mainTarball = npmPack(MAIN_PKG_DIR, packDir);
    const platformTarball = npmPack(PLATFORM_PKG_DIR, packDir);
    npmInstall(installDir, [mainTarball, platformTarball]);

    const installedBin = path.join(
      installDir,
      'node_modules',
      '.bin',
      process.platform === 'win32' ? 'rhizome-mcp.cmd' : 'rhizome-mcp'
    );
    assert.ok(fs.existsSync(installedBin), `expected npm to create a bin shim at ${installedBin}`);

    const spawnOpts = { encoding: 'utf8', shell: process.platform === 'win32' };

    // --version must match the direct binary byte-for-byte, exit code 0.
    const direct = spawnSync(REAL_BINARY_PATH, ['--version'], { encoding: 'utf8' });
    const viaNpm = spawnSync(installedBin, ['--version'], spawnOpts);

    assert.equal(direct.status, 0, `direct binary --version exited ${direct.status}: ${direct.stderr}`);
    assert.equal(viaNpm.status, 0, `installed launcher --version exited ${viaNpm.status}: ${viaNpm.stderr}`);
    assert.equal(viaNpm.stdout, direct.stdout, 'stdout of npx-installed rhizome-mcp must match the raw binary');

    // A bad invocation must exit with the exact same non-zero code both ways
    // (exit-code passthrough), proving the launcher isn't swallowing it.
    const directBad = spawnSync(REAL_BINARY_PATH, ['totally-bogus-subcommand'], { encoding: 'utf8' });
    const viaNpmBad = spawnSync(installedBin, ['totally-bogus-subcommand'], spawnOpts);

    assert.notEqual(directBad.status, 0, 'expected the direct binary to fail on a bad subcommand');
    assert.equal(
      viaNpmBad.status,
      directBad.status,
      'installed launcher must forward the same exit code as the direct binary'
    );
  } finally {
    fs.rmSync(workDir, { recursive: true, force: true });
  }
});
