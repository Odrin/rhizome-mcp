'use strict';

/**
 * Pure, dependency-free helpers for mapping the running process's
 * platform/arch onto the `@rhizome-mcp/<os>-<cpu>` optional dependency that
 * ships the actual Go binary, and for locating that binary inside the
 * resolved package.
 *
 * Kept separate from bin/launcher.js so the mapping/resolution logic can be
 * unit tested without spawning any child process.
 */

// Node's process.platform / process.arch values mapped to the npm/CPU
// naming convention used for the six platform packages.
const PLATFORM_PACKAGES = Object.freeze({
  'darwin-x64': '@rhizome-mcp/darwin-x64',
  'darwin-arm64': '@rhizome-mcp/darwin-arm64',
  'linux-x64': '@rhizome-mcp/linux-x64',
  'linux-arm64': '@rhizome-mcp/linux-arm64',
  'win32-x64': '@rhizome-mcp/win32-x64',
  'win32-arm64': '@rhizome-mcp/win32-arm64',
});

/**
 * @param {string} platform - e.g. process.platform ("darwin", "linux", "win32")
 * @param {string} arch - e.g. process.arch ("x64", "arm64")
 * @returns {string} the "<platform>-<arch>" lookup key
 */
function getPlatformKey(platform, arch) {
  return `${platform}-${arch}`;
}

/**
 * @param {string} platform
 * @param {string} arch
 * @returns {string|null} the `@rhizome-mcp/<os>-<cpu>` package name, or null
 *   if this platform/arch combination is not published.
 */
function getPlatformPackageName(platform, arch) {
  return PLATFORM_PACKAGES[getPlatformKey(platform, arch)] || null;
}

/**
 * @param {string} platform
 * @returns {string} the binary's path relative to the platform package root.
 */
function getBinaryRelativePath(platform) {
  return platform === 'win32' ? 'bin/rhizome-mcp.exe' : 'bin/rhizome-mcp';
}

/**
 * Resolves the absolute path to the platform-specific rhizome-mcp binary.
 *
 * @param {string} platform - process.platform
 * @param {string} arch - process.arch
 * @param {(request: string) => string} resolveFn - injectable in place of
 *   require.resolve so this is unit-testable without real installed packages.
 * @returns {{ ok: true, packageName: string, binaryPath: string } |
 *           { ok: false, reason: 'unsupported-platform'|'package-not-found', packageName: string|null }}
 */
function resolveBinaryPath(platform, arch, resolveFn) {
  const packageName = getPlatformPackageName(platform, arch);
  if (!packageName) {
    return { ok: false, reason: 'unsupported-platform', packageName: null };
  }

  const relativePath = getBinaryRelativePath(platform);
  try {
    const binaryPath = resolveFn(`${packageName}/${relativePath}`);
    return { ok: true, packageName, binaryPath };
  } catch {
    return { ok: false, reason: 'package-not-found', packageName };
  }
}

module.exports = {
  PLATFORM_PACKAGES,
  getPlatformKey,
  getPlatformPackageName,
  getBinaryRelativePath,
  resolveBinaryPath,
};
