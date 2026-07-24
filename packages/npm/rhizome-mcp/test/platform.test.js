'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  getPlatformPackageName,
  getBinaryRelativePath,
  resolveBinaryPath,
} = require('../lib/platform.js');

test('getPlatformPackageName maps every supported platform/arch pair', () => {
  assert.equal(getPlatformPackageName('darwin', 'x64'), '@rhizome-mcp/darwin-x64');
  assert.equal(getPlatformPackageName('darwin', 'arm64'), '@rhizome-mcp/darwin-arm64');
  assert.equal(getPlatformPackageName('linux', 'x64'), '@rhizome-mcp/linux-x64');
  assert.equal(getPlatformPackageName('linux', 'arm64'), '@rhizome-mcp/linux-arm64');
  assert.equal(getPlatformPackageName('win32', 'x64'), '@rhizome-mcp/win32-x64');
  assert.equal(getPlatformPackageName('win32', 'arm64'), '@rhizome-mcp/win32-arm64');
});

test('getPlatformPackageName returns null for unsupported platform/arch pairs', () => {
  assert.equal(getPlatformPackageName('sunos', 'x64'), null);
  assert.equal(getPlatformPackageName('linux', 'ia32'), null);
  assert.equal(getPlatformPackageName('darwin', 'ia32'), null);
  assert.equal(getPlatformPackageName('freebsd', 'arm64'), null);
});

test('getBinaryRelativePath appends .exe only on win32', () => {
  assert.equal(getBinaryRelativePath('win32'), 'bin/rhizome-mcp.exe');
  assert.equal(getBinaryRelativePath('darwin'), 'bin/rhizome-mcp');
  assert.equal(getBinaryRelativePath('linux'), 'bin/rhizome-mcp');
});

test('resolveBinaryPath succeeds when the injected resolver finds the binary', () => {
  const fakeResolve = (request) => {
    assert.equal(request, '@rhizome-mcp/linux-x64/bin/rhizome-mcp');
    return '/fake/node_modules/@rhizome-mcp/linux-x64/bin/rhizome-mcp';
  };

  const result = resolveBinaryPath('linux', 'x64', fakeResolve);
  assert.deepEqual(result, {
    ok: true,
    packageName: '@rhizome-mcp/linux-x64',
    binaryPath: '/fake/node_modules/@rhizome-mcp/linux-x64/bin/rhizome-mcp',
  });
});

test('resolveBinaryPath uses the .exe path on win32', () => {
  const fakeResolve = (request) => {
    assert.equal(request, '@rhizome-mcp/win32-x64/bin/rhizome-mcp.exe');
    return '/fake/node_modules/@rhizome-mcp/win32-x64/bin/rhizome-mcp.exe';
  };

  const result = resolveBinaryPath('win32', 'x64', fakeResolve);
  assert.equal(result.ok, true);
  assert.equal(result.binaryPath, '/fake/node_modules/@rhizome-mcp/win32-x64/bin/rhizome-mcp.exe');
});

test('resolveBinaryPath reports unsupported-platform without calling resolveFn', () => {
  let called = false;
  const fakeResolve = () => {
    called = true;
    throw new Error('should not be called');
  };

  const result = resolveBinaryPath('sunos', 'x64', fakeResolve);
  assert.equal(called, false);
  assert.deepEqual(result, { ok: false, reason: 'unsupported-platform', packageName: null });
});

test('resolveBinaryPath reports package-not-found when the resolver throws (e.g. --no-optional install)', () => {
  const fakeResolve = () => {
    const err = new Error("Cannot find module '@rhizome-mcp/darwin-arm64/bin/rhizome-mcp'");
    err.code = 'MODULE_NOT_FOUND';
    throw err;
  };

  const result = resolveBinaryPath('darwin', 'arm64', fakeResolve);
  assert.deepEqual(result, {
    ok: false,
    reason: 'package-not-found',
    packageName: '@rhizome-mcp/darwin-arm64',
  });
});
