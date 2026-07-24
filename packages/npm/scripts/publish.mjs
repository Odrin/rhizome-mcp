#!/usr/bin/env node
/**
 * publish.mjs
 *
 * Publishes all 7 rhizome-mcp npm packages (6 `@rhizome-mcp/<os>-<cpu>`
 * platform packages + the `rhizome-mcp` main launcher package) for a given
 * release tag. Node core modules only — no npm dependencies. Meant to be
 * called from `.github/workflows/release.yml`'s `publish-npm` job, but is
 * fully runnable (and safe, via --dry-run) from a local checkout too.
 *
 * ---------------------------------------------------------------------------
 * INPUT CONTRACT (--binaries-dir)
 * ---------------------------------------------------------------------------
 * This script does NOT build Go binaries and does NOT know anything about
 * .github/workflows/release.yml's own release-asset naming
 * (`rhizome-mcp_<version>_<goos>_<goarch>.tar.gz`/`.zip`). It expects a flat
 * directory of already-built, already-extracted Go binaries named exactly
 * (mirroring editors/vscode/scripts/package-platforms.mjs's convention):
 *
 *   rhizome-mcp_<goos>_<goarch>        (darwin, linux)
 *   rhizome-mcp_<goos>_<goarch>.exe    (windows)
 *
 * i.e. Go's own GOOS/GOARCH values joined by underscores, with `.exe` added
 * only for windows, and NO version number in the filename. All six must be
 * present:
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
 * invoking this script. The `publish-npm` job in release.yml does exactly
 * this before calling here.
 *
 * ---------------------------------------------------------------------------
 * VERSION POLICY
 * ---------------------------------------------------------------------------
 * Unlike the VS Code Marketplace, npm accepts semver prerelease suffixes
 * directly. So, unlike package-platforms.mjs's odd/even remapping, the
 * published npm `version` is just the git tag with its leading `v` stripped,
 * verbatim:
 *
 *   v1.0.0-beta.3  ->  1.0.0-beta.3
 *   v1.0.0         ->  1.0.0
 *
 * The tag is taken from --tag (required; no environment-variable or
 * `git describe` fallback, unlike package-platforms.mjs — this script is
 * only ever meant to run against a real GitHub release event, where the tag
 * is unambiguous, so we require it explicitly rather than guessing).
 *
 * The main `rhizome-mcp` package's `optionalDependencies` pin an exact
 * version of each of the 6 platform packages. Those pins are bumped to the
 * same derived version alongside `version` itself, so the main package
 * being published never points its optional deps at a stale/placeholder
 * platform version.
 *
 * ---------------------------------------------------------------------------
 * DIST-TAG POLICY (the "should `latest` point at beta" decision)
 * ---------------------------------------------------------------------------
 * Every package is published with exactly ONE `npm publish --tag <x>` call
 * and nothing else — no follow-up `npm dist-tag add`. Earlier versions of
 * this script tried to publish beta releases under `--tag beta` and then
 * separately move `latest` with `npm dist-tag add` when appropriate; that
 * failed in real CI with a 401, because npm's OIDC Trusted Publishing only
 * authenticates the `npm publish` command itself — it does not extend to
 * other authenticated registry writes run afterward in the same job. So
 * the dist-tag decision has to be folded into the one publish call:
 *
 * - Stable tag (no `-beta.N` suffix): always `--tag latest`.
 * - Beta tag (`-beta.N` suffix): `--tag latest` UNTIL a real stable version
 *   has ever been published for that package, then `--tag beta` from then
 *   on.
 *
 * Why betas chase `latest` pre-stable: `npx rhizome-mcp` / `npm install
 * rhizome-mcp` both resolve the `latest` dist-tag by default, and all 7
 * packages currently have `latest` pointing at a non-functional `0.0.1`
 * placeholder (published manually before this pipeline existed). Leaving
 * `latest` stuck there while every real release ships under `beta` would
 * make the plain `npx rhizome-mcp` command useless for a long time (this
 * project is pre-1.0, so betas may be the only thing shipping for a while).
 * Once a real stable version exists, betas stop touching `latest` — from
 * then on only stable publishes move it, and `--no-latest-follow` forces
 * `--tag beta` regardless (a beta version can never itself carry `latest`
 * once this flag is set).
 *
 * "Has a real stable version ever been published" is detected per-package
 * via `npm view <pkg> versions --json`: any returned version that (a) does
 * not match `-beta.N` and (b) is not the known bootstrap placeholder
 * `0.0.1` counts as a real stable release. The `0.0.1` placeholder is
 * special-cased out deliberately — it was published by hand before this
 * pipeline existed, is not a functional release (no bin/ payload), and
 * treating its presence as "stable already shipped" would permanently
 * disable the latest-follows-beta behavior this policy exists for, even
 * though no real release has gone out yet. See BOOTSTRAP_PLACEHOLDER_VERSION
 * below.
 *
 * ---------------------------------------------------------------------------
 * IDEMPOTENCY
 * ---------------------------------------------------------------------------
 * Before publishing each package, this script runs
 * `npm view <pkg>@<version> version`. If that succeeds, the version is
 * already on the registry (e.g. a re-run of a partially-failed release job)
 * and the package is skipped (logged, not an error). Only a 404 from
 * `npm view` is treated as "not yet published".
 *
 * ---------------------------------------------------------------------------
 * PUBLISH ORDER
 * ---------------------------------------------------------------------------
 * All 6 platform packages are published first, then the `rhizome-mcp` main
 * package last — so `optionalDependencies` on the main package are never
 * left dangling (pointing at a platform version that isn't on the registry
 * yet) partway through a publish.
 *
 * ---------------------------------------------------------------------------
 * USAGE
 * ---------------------------------------------------------------------------
 *   node scripts/publish.mjs --binaries-dir /path/to/binaries --tag v1.0.0-beta.3 [--dry-run]
 *
 * Flags:
 *   --binaries-dir <dir>   Required unless --skip-staging. See INPUT CONTRACT.
 *   --tag <tag>            Required. Git release tag, e.g. v1.0.0-beta.3 or v1.0.0.
 *   --dry-run              Pass --dry-run to `npm publish` (packs + validates,
 *                          does NOT upload). Staging, package.json version-
 *                          stamping, and the read-only `npm view` checks
 *                          (idempotency + stable-shipped detection) still
 *                          run for real — they're safe reads (or, for
 *                          staging/stamping, local-disk-only writes).
 *   --skip-staging         Skip the binary-staging step (assumes bin/ payloads
 *                          are already staged in each platform package, e.g.
 *                          for re-running just the publish logic).
 *   --no-latest-follow     Forces --tag beta for beta publishes regardless
 *                          of the stable-shipped detection (see DIST-TAG
 *                          POLICY above). Escape hatch for manual intervention.
 *   --npm-root <dir>       Root directory containing the 7 package dirs.
 *                          Defaults to the packages/npm/ directory this
 *                          script lives under.
 *
 * Per package, this script: stages the right binary into bin/ (unless
 * --skip-staging), stamps package.json's version, checks whether that
 * version is already published (skip if so), and publishes once with the
 * single dist-tag the DIST-TAG POLICY above decides — no separate
 * dist-tag-add call, ever.
 */

import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  copyFileSync,
  chmodSync,
} from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_NPM_ROOT = path.resolve(__dirname, '..');

// The one-time, hand-published placeholder version every one of the 7
// packages currently sits at. Never counts as a "real stable release" when
// deciding whether beta publishes should also chase the `latest` dist-tag.
const BOOTSTRAP_PLACEHOLDER_VERSION = '0.0.1';

// Go GOOS/GOARCH triple -> platform package directory name (matches
// packages/npm/rhizome-mcp/lib/platform.js's PLATFORM_PACKAGES mapping and
// editors/vscode/scripts/package-platforms.mjs's TARGET_MAP naming).
const BINARY_TO_PKG_DIR = {
  'darwin-amd64': 'darwin-x64',
  'darwin-arm64': 'darwin-arm64',
  'linux-amd64': 'linux-x64',
  'linux-arm64': 'linux-arm64',
  'windows-amd64': 'win32-x64',
  'windows-arm64': 'win32-arm64',
};

const WINDOWS_PKG_DIRS = new Set(['win32-x64', 'win32-arm64']);

// Publish order: all 6 platform packages first (order among themselves
// doesn't matter), `rhizome-mcp` last (see PUBLISH ORDER above).
const MAIN_PKG_DIR = 'rhizome-mcp';
const PLATFORM_PKG_DIRS = Object.values(BINARY_TO_PKG_DIR);
const PUBLISH_ORDER = [...PLATFORM_PKG_DIRS, MAIN_PKG_DIR];

function printHelp() {
  console.log(`Usage:
  node scripts/publish.mjs --binaries-dir <dir> --tag <tag> [--dry-run] [--skip-staging] [--no-latest-follow] [--npm-root <dir>]

See the header comment of this file for the full input contract, version
policy, dist-tag policy, and idempotency behavior.`);
}

function parseArgs(argv) {
  const opts = {
    binariesDir: null,
    tag: null,
    dryRun: false,
    skipStaging: false,
    noLatestFollow: false,
    npmRoot: DEFAULT_NPM_ROOT,
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    switch (arg) {
      case '--binaries-dir':
        opts.binariesDir = argv[++i];
        break;
      case '--tag':
        opts.tag = argv[++i];
        break;
      case '--dry-run':
        opts.dryRun = true;
        break;
      case '--skip-staging':
        opts.skipStaging = true;
        break;
      case '--no-latest-follow':
        opts.noLatestFollow = true;
        break;
      case '--npm-root':
        opts.npmRoot = argv[++i];
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

/**
 * Parses a release tag into an npm version + beta flag, per VERSION POLICY.
 */
function deriveVersion(tag) {
  const match = /^v(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?$/.exec(tag);
  if (!match) {
    throw new Error(
      `Tag "${tag}" does not match the expected vMAJOR.MINOR.PATCH[-beta.N] format.`,
    );
  }
  const isBeta = match[4] !== undefined;
  const version = tag.slice(1); // strip leading 'v', keep the rest verbatim
  return { version, isBeta };
}

function run(cmd, args, cwd) {
  console.log(`[publish] $ ${cmd} ${args.join(' ')} (cwd=${cwd})`);
  const result = spawnSync(cmd, args, { cwd, stdio: 'inherit', encoding: 'utf8' });
  if (result.error) {
    throw result.error;
  }
  return result.status === 0;
}

function runCapture(cmd, args, cwd) {
  const result = spawnSync(cmd, args, { cwd, encoding: 'utf8' });
  return {
    ok: result.status === 0,
    stdout: (result.stdout || '').trim(),
    stderr: (result.stderr || '').trim(),
  };
}

function binaryFileName(goos, goarch) {
  return `rhizome-mcp_${goos}_${goarch}${goos === 'windows' ? '.exe' : ''}`;
}

/**
 * Stages every platform package's bin/rhizome-mcp[.exe] from the flat
 * --binaries-dir input, chmod +x on non-Windows binaries.
 */
function stageBinaries(binariesDir, npmRoot) {
  for (const [goTriple, pkgDir] of Object.entries(BINARY_TO_PKG_DIR)) {
    const [goos, goarch] = goTriple.split('-');
    const srcBinary = path.join(binariesDir, binaryFileName(goos, goarch));
    if (!existsSync(srcBinary)) {
      throw new Error(`Missing input binary for ${goTriple}: expected ${srcBinary}`);
    }

    const destDir = path.join(npmRoot, pkgDir, 'bin');
    mkdirSync(destDir, { recursive: true });
    const destName = WINDOWS_PKG_DIRS.has(pkgDir) ? 'rhizome-mcp.exe' : 'rhizome-mcp';
    const destBinary = path.join(destDir, destName);
    copyFileSync(srcBinary, destBinary);
    if (!WINDOWS_PKG_DIRS.has(pkgDir)) {
      chmodSync(destBinary, 0o755);
    }
    console.log(`[publish] staged ${pkgDir}/bin/${destName} <- ${srcBinary}`);
  }
}

/**
 * Stamps `version` into all 7 package.json files, verbatim. The main
 * package's `optionalDependencies` pin exact versions of the 6 platform
 * packages, so those must move in lockstep with `version` too — otherwise
 * a published main package would keep pointing its optional deps at the
 * old (or placeholder) platform version instead of the one actually being
 * published alongside it.
 */
function stampVersions(npmRoot, version) {
  for (const pkgDir of PUBLISH_ORDER) {
    const pkgPath = path.join(npmRoot, pkgDir, 'package.json');
    const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
    pkg.version = version;
    if (pkgDir === MAIN_PKG_DIR && pkg.optionalDependencies) {
      for (const depName of Object.keys(pkg.optionalDependencies)) {
        pkg.optionalDependencies[depName] = version;
      }
    }
    writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + '\n');
    console.log(`[publish] stamped ${pkgDir}/package.json -> version ${version}`);
  }
}

function readPackageName(npmRoot, pkgDir) {
  const pkgPath = path.join(npmRoot, pkgDir, 'package.json');
  return JSON.parse(readFileSync(pkgPath, 'utf8')).name;
}

/**
 * True if `<name>@<version>` already exists on the registry (idempotency
 * check — read-only, always run for real regardless of --dry-run).
 */
function isAlreadyPublished(name, version) {
  const { ok } = runCapture('npm', ['view', `${name}@${version}`, 'version']);
  return ok;
}

/**
 * True if any version of `name` on the registry is a real stable release
 * (not the bootstrap placeholder, not a -beta.N prerelease). Read-only —
 * always run for real regardless of --dry-run. See DIST-TAG POLICY above.
 */
function hasRealStableVersionShipped(name) {
  const { ok, stdout } = runCapture('npm', ['view', name, 'versions', '--json']);
  if (!ok || !stdout) {
    // Package has no published versions at all (or doesn't exist yet) ->
    // no stable version has shipped.
    return false;
  }
  let versions;
  try {
    const parsed = JSON.parse(stdout);
    versions = Array.isArray(parsed) ? parsed : [parsed];
  } catch {
    return false;
  }
  return versions.some(
    (v) => v !== BOOTSTRAP_PLACEHOLDER_VERSION && !/-beta\.\d+$/.test(v),
  );
}

/**
 * Decides the single `--tag` argument to publish with. There is
 * deliberately no separate `npm dist-tag add` call anywhere in this
 * script: npm's OIDC Trusted Publishing authenticates the `npm publish`
 * command specifically — it does NOT extend to other authenticated
 * registry writes (like `npm dist-tag add`) run afterward in the same
 * job, which fail with a 401 (confirmed against a real CI run: `npm
 * publish --provenance` succeeded via OIDC, the follow-up `npm dist-tag
 * add` immediately failed with "Unable to authenticate"). So the
 * "should this land on `latest`" decision has to be folded into the one
 * authenticated call this script ever makes.
 */
function decidePublishTag(name, isBeta, opts) {
  if (!isBeta) {
    return 'latest';
  }
  if (opts.noLatestFollow) {
    return 'beta';
  }
  const stableShipped = hasRealStableVersionShipped(name);
  return stableShipped ? 'beta' : 'latest';
}

function publishPackage(npmRoot, pkgDir, version, isBeta, opts) {
  const cwd = path.join(npmRoot, pkgDir);
  const name = readPackageName(npmRoot, pkgDir);
  const fullRef = `${name}@${version}`;

  if (isAlreadyPublished(name, version)) {
    console.log(`[publish] SKIP ${fullRef} - already published`);
    return;
  }

  const distTag = decidePublishTag(name, isBeta, opts);
  const publishArgs = ['publish', '--provenance', '--tag', distTag];
  if (opts.dryRun) {
    publishArgs.push('--dry-run');
  }

  const published = run('npm', publishArgs, cwd);
  if (!published) {
    throw new Error(`npm publish failed for ${fullRef}`);
  }
  console.log(`[publish] published ${fullRef} (tag=${distTag}${opts.dryRun ? ', dry-run' : ''})`);
}

function main() {
  const opts = parseArgs(process.argv.slice(2));

  if (!opts.tag) {
    throw new Error('--tag is required. See --help.');
  }
  if (!opts.skipStaging && !opts.binariesDir) {
    throw new Error('--binaries-dir is required (or use --skip-staging). See --help.');
  }

  const npmRoot = path.resolve(opts.npmRoot);
  const { version, isBeta } = deriveVersion(opts.tag);
  console.log(
    `[publish] tag=${opts.tag} version=${version} isBeta=${isBeta} dryRun=${opts.dryRun} npmRoot=${npmRoot}`,
  );

  if (!opts.skipStaging) {
    stageBinaries(path.resolve(opts.binariesDir), npmRoot);
  }

  stampVersions(npmRoot, version);

  for (const pkgDir of PUBLISH_ORDER) {
    publishPackage(npmRoot, pkgDir, version, isBeta, opts);
  }

  console.log('[publish] done');
}

main();
