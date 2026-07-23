#!/usr/bin/env node
/**
 * package-platforms.mjs
 *
 * Builds one platform-specific .vsix of the Rhizome MCP VS Code extension per
 * Marketplace target, each bundling the matching platform's `rhizome-mcp` Go
 * binary at `extension/bin/rhizome-mcp[.exe]` (the path/name the extension's
 * binary resolver expects). Node core modules only — no npm dependencies.
 *
 * ---------------------------------------------------------------------------
 * INPUT CONTRACT (--binaries-dir)
 * ---------------------------------------------------------------------------
 * This script does NOT build Go binaries itself (except in --local mode, see
 * below) and does NOT know anything about .github/workflows/release.yml's own
 * asset naming (`rhizome-mcp_<version>_<goos>_<goarch>.tar.gz`/`.zip`). It
 * expects a flat directory of already-built, already-extracted Go binaries
 * named exactly:
 *
 *   rhizome-mcp_<goos>_<goarch>        (darwin, linux)
 *   rhizome-mcp_<goos>_<goarch>.exe    (windows)
 *
 * i.e. Go's own GOOS/GOARCH values joined by underscores, with `.exe` added
 * only for windows, and NO version number in the filename. For the full
 * 8-target build this directory must contain all six:
 *
 *   rhizome-mcp_darwin_amd64
 *   rhizome-mcp_darwin_arm64
 *   rhizome-mcp_linux_amd64
 *   rhizome-mcp_linux_arm64
 *   rhizome-mcp_windows_amd64.exe
 *   rhizome-mcp_windows_arm64.exe
 *
 * A caller (CI or a human) that only has release.yml's tarballs/zips must
 * extract each archive's single binary and rename it into this layout before
 * invoking this script. This is a deliberate seam: release.yml's archive
 * naming carries a version number for GitHub release assets; this script's
 * input naming does not, because it runs once per publish against whatever
 * binaries happen to be on disk, and version is a separate concern (see
 * VERSION POLICY below), stamped into package.json, not read from filenames.
 *
 * ---------------------------------------------------------------------------
 * TARGET MAPPING (Go binary -> VS Code Marketplace --target)
 * ---------------------------------------------------------------------------
 * The Go binary is CGO-free/static, so the linux binary also serves Alpine
 * as-is: one binary, two `vsce --target` values.
 *
 *   darwin/amd64   -> darwin-x64
 *   darwin/arm64   -> darwin-arm64
 *   linux/amd64    -> linux-x64, alpine-x64
 *   linux/arm64    -> linux-arm64, alpine-arm64
 *   windows/amd64  -> win32-x64
 *   windows/arm64  -> win32-arm64
 *
 * That's 8 VSIX targets built from 6 Go binaries.
 *
 * ---------------------------------------------------------------------------
 * VERSION POLICY
 * ---------------------------------------------------------------------------
 * The Marketplace requires a plain `major.minor.patch` version (no semver
 * prerelease suffix), but this project's git tags look like
 * `v1.0.0-beta.3`. This script derives the tag from (in priority order):
 *   1. --tag <tag> on the command line
 *   2. the RHIZOME_RELEASE_TAG environment variable
 *   3. `git describe --tags --abbrev=0` run at the repo root
 * and maps it as follows:
 *
 *   - Beta tag `vMAJOR.MINOR.PATCH-beta.N` (pre-release channel):
 *       marketplace version = MAJOR.(MINOR*2+1).(PATCH*1000+N)
 *       published with `vsce package --pre-release`
 *     Forcing the minor number odd follows the Marketplace's own documented
 *     convention that pre-release builds use an odd minor version to keep
 *     them in a separate update channel from stable. The `*1000+N` patch
 *     encoding keeps successive beta publishes (and any future patch bump
 *     while still in beta) monotonically increasing, which the Marketplace
 *     requires for each new publish of the same target.
 *
 *   - Stable tag `vMAJOR.MINOR.PATCH` (no `-beta.N` suffix):
 *       marketplace version = MAJOR.MINOR.PATCH, published WITHOUT
 *       `--pre-release` (stable channel).
 *     Once the server ships a stable release, this extension's version
 *     locks step with the server's own version, verbatim, forever after
 *     (no more odd/even remapping).
 *
 * If no tag can be resolved at all (e.g. a shallow local checkout with no
 * tags), the script leaves package.json's existing `version` field alone and
 * warns, rather than failing outright — this keeps `npm run package:local`
 * usable in a fresh clone.
 *
 * ---------------------------------------------------------------------------
 * USAGE
 * ---------------------------------------------------------------------------
 *   # Full release: build all 8 VSIXes from a directory of 6 binaries
 *   node scripts/package-platforms.mjs \
 *     --binaries-dir /path/to/binaries --out /path/to/vsixes [--tag vX.Y.Z[-beta.N]]
 *
 *   # Single target (e.g. one-off manual test)
 *   node scripts/package-platforms.mjs \
 *     --binaries-dir /path/to/binaries --out /path/to/vsixes --vsce-target darwin-arm64
 *
 *   # Local dev: build the current platform's Go binary and package just it
 *   # (this is what `npm run package:local` runs)
 *   node scripts/package-platforms.mjs --local [--out /path/to/vsixes]
 *
 * Per target this script: stages the right binary into bin/, chmods it 0o755
 * on non-Windows targets, stamps package.json's version, runs
 * `vsce package --target <target> [--pre-release]`, then restores bin/ and
 * package.json to their original (clean) state before moving to the next
 * target — so no target's build can leak into another's.
 */

import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  copyFileSync,
  chmodSync,
  rmSync,
  mkdtempSync,
} from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const VSCODE_DIR = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(VSCODE_DIR, '..', '..');
const PKG_PATH = path.join(VSCODE_DIR, 'package.json');
const BIN_DIR = path.join(VSCODE_DIR, 'bin');

// Go binary triple -> list of `vsce --target` values it serves. Order
// matters: index 0 is treated as the "native" (non-Alpine) desktop target
// for --local mode.
const TARGET_MAP = {
  'darwin-amd64': ['darwin-x64'],
  'darwin-arm64': ['darwin-arm64'],
  'linux-amd64': ['linux-x64', 'alpine-x64'],
  'linux-arm64': ['linux-arm64', 'alpine-arm64'],
  'windows-amd64': ['win32-x64'],
  'windows-arm64': ['win32-arm64'],
};

const WIN32_TARGETS = new Set(['win32-x64', 'win32-arm64']);

// A shell is only needed to resolve .cmd shims (npm/npx/go on Windows);
// on POSIX, spawning without a shell avoids re-quoting argv (which matters
// for paths containing spaces) and Node's shell-arg-escaping warning.
const USE_SHELL = process.platform === 'win32';

function printHelp() {
  console.log(`Usage:
  node scripts/package-platforms.mjs --binaries-dir <dir> --out <dir> [--tag <tag>] [--vsce-target <target>]
  node scripts/package-platforms.mjs --local [--out <dir>]

See the header comment of this file for the full input contract, target
mapping, and version policy.`);
}

function parseArgs(argv) {
  const opts = {
    binariesDir: null,
    out: null,
    tag: null,
    vsceTarget: null,
    local: false,
    preRelease: undefined,
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    switch (arg) {
      case '--binaries-dir':
        opts.binariesDir = argv[++i];
        break;
      case '--out':
        opts.out = argv[++i];
        break;
      case '--tag':
        opts.tag = argv[++i];
        break;
      case '--vsce-target':
        opts.vsceTarget = argv[++i];
        break;
      case '--local':
        opts.local = true;
        break;
      case '--pre-release':
        opts.preRelease = true;
        break;
      case '--no-pre-release':
        opts.preRelease = false;
        break;
      case '--help':
      case '-h':
        printHelp();
        process.exit(0);
        break;
      default:
        throw new Error(`Unknown argument: ${arg} (see --help)`);
    }
  }
  return opts;
}

function run(cmd, args, cwd, extraEnv) {
  console.log(`[package-platforms] $ ${cmd} ${args.join(' ')}`);
  const env = extraEnv ? { ...process.env, ...extraEnv } : process.env;
  const result = spawnSync(cmd, args, { cwd, stdio: 'inherit', shell: USE_SHELL, env });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`Command failed (exit ${result.status}): ${cmd} ${args.join(' ')}`);
  }
}

function captureGoEnv(varName) {
  const result = spawnSync('go', ['env', varName], { cwd: REPO_ROOT, encoding: 'utf8', shell: USE_SHELL });
  if (result.status !== 0) {
    throw new Error(`\`go env ${varName}\` failed: ${result.stderr}`);
  }
  return result.stdout.trim();
}

function binaryFileName(goos, goarch) {
  return `rhizome-mcp_${goos}_${goarch}${goos === 'windows' ? '.exe' : ''}`;
}

/**
 * Resolves the marketplace version + pre-release flag to use, per the
 * VERSION POLICY documented above.
 */
function deriveVersion({ tagOverride, fallbackVersion }) {
  let tag = tagOverride || process.env.RHIZOME_RELEASE_TAG || null;
  let tagSource = tagOverride ? '--tag' : process.env.RHIZOME_RELEASE_TAG ? 'RHIZOME_RELEASE_TAG' : null;
  if (!tag) {
    const result = spawnSync('git', ['describe', '--tags', '--abbrev=0'], {
      cwd: REPO_ROOT,
      encoding: 'utf8',
      shell: USE_SHELL,
    });
    if (result.status === 0 && result.stdout.trim()) {
      tag = result.stdout.trim();
      tagSource = 'git describe --tags --abbrev=0';
    }
  }

  if (!tag) {
    console.warn(
      `[package-platforms] No git tag resolved (no --tag, no RHIZOME_RELEASE_TAG, and \`git describe\` found none). ` +
        `Keeping package.json's existing version (${fallbackVersion}) unchanged.`,
    );
    return { version: fallbackVersion, preRelease: false, tag: null };
  }

  const match = /^v(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?$/.exec(tag);
  if (!match) {
    throw new Error(
      `Tag "${tag}" (from ${tagSource}) does not match the expected vMAJOR.MINOR.PATCH[-beta.N] format.`,
    );
  }
  const [, majorStr, minorStr, patchStr, betaStr] = match;
  const major = Number(majorStr);
  const minor = Number(minorStr);
  const patch = Number(patchStr);

  if (betaStr !== undefined) {
    const beta = Number(betaStr);
    const marketplaceMinor = minor * 2 + 1;
    const marketplacePatch = patch * 1000 + beta;
    return {
      version: `${major}.${marketplaceMinor}.${marketplacePatch}`,
      preRelease: true,
      tag,
    };
  }

  return { version: `${major}.${minor}.${patch}`, preRelease: false, tag };
}

function vsceArgsFor(target, outFile, preRelease) {
  const args = ['--no-install', 'vsce', 'package', '--target', target, '--out', outFile];
  if (preRelease) {
    args.push('--pre-release');
  }
  return args;
}

function stageBinary(srcBinary, vsceTarget) {
  rmSync(BIN_DIR, { recursive: true, force: true });
  mkdirSync(BIN_DIR, { recursive: true });
  const destName = WIN32_TARGETS.has(vsceTarget) ? 'rhizome-mcp.exe' : 'rhizome-mcp';
  const destBinary = path.join(BIN_DIR, destName);
  copyFileSync(srcBinary, destBinary);
  if (!WIN32_TARGETS.has(vsceTarget)) {
    chmodSync(destBinary, 0o755);
  }
  return destBinary;
}

/**
 * Builds one .vsix per requested (goTriple, vsceTarget) pair, restoring
 * bin/ and package.json to a clean state between (and after) every target.
 */
function packageTargets({ binariesDir, outDir, pairs, version, preRelease }) {
  mkdirSync(outDir, { recursive: true });
  const originalPkgText = readFileSync(PKG_PATH, 'utf8');

  const built = [];
  try {
    // The JS bundle is identical across targets; build it once.
    run('npm', ['run', 'build', '--', '--production'], VSCODE_DIR);

    for (const { goTriple, vsceTarget } of pairs) {
      const [goos, goarch] = goTriple.split('-');
      const srcBinary = path.join(binariesDir, binaryFileName(goos, goarch));
      if (!existsSync(srcBinary)) {
        throw new Error(`Missing input binary for ${goTriple}: expected ${srcBinary}`);
      }

      stageBinary(srcBinary, vsceTarget);

      const pkg = JSON.parse(originalPkgText);
      pkg.version = version;
      writeFileSync(PKG_PATH, JSON.stringify(pkg, null, 2) + '\n');

      const outFile = path.join(outDir, `rhizome-mcp-${version}-${vsceTarget}.vsix`);
      run('npx', vsceArgsFor(vsceTarget, outFile, preRelease), VSCODE_DIR);
      built.push(outFile);

      // Restore before the next iteration so nothing leaks across targets.
      writeFileSync(PKG_PATH, originalPkgText);
      rmSync(BIN_DIR, { recursive: true, force: true });
    }
  } finally {
    // Always restore, even on failure.
    writeFileSync(PKG_PATH, originalPkgText);
    rmSync(BIN_DIR, { recursive: true, force: true });
  }
  return built;
}

function runLocal(opts) {
  const goos = captureGoEnv('GOOS');
  const goarch = captureGoEnv('GOARCH');
  const goTriple = `${goos}-${goarch}`;
  const vsceTargets = TARGET_MAP[goTriple];
  if (!vsceTargets) {
    throw new Error(`No known Marketplace target for host platform ${goTriple}`);
  }
  const vsceTarget = vsceTargets[0]; // native desktop target, not the Alpine alias

  const scratchDir = mkdtempSync(path.join(os.tmpdir(), 'rhizome-vscode-local-'));
  try {
    const binName = binaryFileName(goos, goarch);
    const binPath = path.join(scratchDir, binName);
    console.log(`[package-platforms] Building local ${goTriple} binary -> ${binPath}`);
    run('go', ['build', '-o', binPath, '.'], REPO_ROOT, { CGO_ENABLED: '0', GOOS: goos, GOARCH: goarch });

    const outDir = opts.out ? path.resolve(opts.out) : VSCODE_DIR;
    const { version, preRelease, tag } = deriveVersion({
      tagOverride: opts.tag,
      fallbackVersion: JSON.parse(readFileSync(PKG_PATH, 'utf8')).version,
    });
    const effectivePreRelease = opts.preRelease !== undefined ? opts.preRelease : preRelease;
    console.log(
      `[package-platforms] local target=${vsceTarget} tag=${tag ?? '(none)'} version=${version} preRelease=${effectivePreRelease}`,
    );

    const built = packageTargets({
      binariesDir: scratchDir,
      outDir,
      pairs: [{ goTriple, vsceTarget }],
      version,
      preRelease: effectivePreRelease,
    });
    console.log(`[package-platforms] Built local package:\n  - ${built[0]}`);
  } finally {
    rmSync(scratchDir, { recursive: true, force: true });
  }
}

function main() {
  const opts = parseArgs(process.argv.slice(2));

  if (opts.local) {
    runLocal(opts);
    return;
  }

  if (!opts.binariesDir) {
    throw new Error('--binaries-dir is required (or use --local). See --help.');
  }
  if (!opts.out) {
    throw new Error('--out is required (or use --local). See --help.');
  }

  const binariesDir = path.resolve(opts.binariesDir);
  const outDir = path.resolve(opts.out);

  const { version, preRelease, tag } = deriveVersion({
    tagOverride: opts.tag,
    fallbackVersion: JSON.parse(readFileSync(PKG_PATH, 'utf8')).version,
  });
  const effectivePreRelease = opts.preRelease !== undefined ? opts.preRelease : preRelease;
  console.log(
    `[package-platforms] tag=${tag ?? '(none)'} version=${version} preRelease=${effectivePreRelease}`,
  );

  let pairs = [];
  for (const [goTriple, vsceTargets] of Object.entries(TARGET_MAP)) {
    for (const vsceTarget of vsceTargets) {
      pairs.push({ goTriple, vsceTarget });
    }
  }
  if (opts.vsceTarget) {
    pairs = pairs.filter((p) => p.vsceTarget === opts.vsceTarget);
    if (pairs.length === 0) {
      throw new Error(`Unknown --vsce-target "${opts.vsceTarget}"`);
    }
  }

  const built = packageTargets({ binariesDir, outDir, pairs, version, preRelease: effectivePreRelease });
  console.log(`[package-platforms] Built ${built.length} package(s):`);
  for (const f of built) {
    console.log(`  - ${f}`);
  }
}

main();
