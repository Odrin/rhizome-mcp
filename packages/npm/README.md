# packages/npm

npm distribution for rhizome-mcp, so it can be run via `npx rhizome-mcp`
without a Go toolchain.

## Layout

```
packages/npm/
  rhizome-mcp/        main package - published as "rhizome-mcp"
    package.json
    bin/launcher.js   dependency-free Node launcher (no build step)
    lib/platform.js   platform/arch -> package-name mapping (unit tested)
    test/             node:test suite (unit + install/smoke tests)
    README.md

  darwin-x64/          published as "@rhizome-mcp/darwin-x64"
  darwin-arm64/        published as "@rhizome-mcp/darwin-arm64"
  linux-x64/            published as "@rhizome-mcp/linux-x64"
  linux-arm64/          published as "@rhizome-mcp/linux-arm64"
  win32-x64/            published as "@rhizome-mcp/win32-x64"
  win32-arm64/          published as "@rhizome-mcp/win32-arm64"
```

Each of the six platform directories ships (or, pre-release, will ship) a
single prebuilt Go binary at `bin/rhizome-mcp` (`bin/rhizome-mcp.exe` on
win32) plus a minimal `package.json` declaring `os`/`cpu` so npm installs
exactly one of the six for any given machine, via the main package's
`optionalDependencies`.

## How resolution works

`rhizome-mcp`'s `bin/launcher.js` has no dependencies and does no build
step. At run time it maps `process.platform`/`process.arch` to one of the
six platform package names, `require.resolve()`s that package's
`bin/rhizome-mcp[.exe]`, and `spawn()`s it with `stdio: 'inherit'`,
forwarding argv, the exit code, and `SIGTERM`/`SIGINT`. If the matching
platform package isn't installed (unsupported platform, or an install with
`--no-optional`/`--ignore-scripts` that skipped optional dependencies), it
prints one actionable error to stderr and exits 1 instead of throwing.

None of the 7 packages have `preinstall`/`install`/`postinstall` scripts.
Resolution happens lazily when the binary is invoked, not at install time.

## Binaries in this checkout

This repository does not commit real cross-platform binaries for all six
platform packages - a Go toolchain here can only meaningfully build and
smoke-test the local dev machine's own platform. As of this writing, only
`darwin-arm64/bin/rhizome-mcp` contains a real, locally-built binary (via
`CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ...`), used for the
node:test smoke test.

The other five platform packages currently ship only their `package.json`
shape, with no populated `bin/` payload. Populating all six with real
release binaries and publishing all 7 packages to npm in lockstep is done
by `.github/workflows/release.yml`'s `publish-npm` job, via
`scripts/publish.mjs` (see below).

## Publishing (`scripts/publish.mjs`)

`packages/npm/scripts/publish.mjs` is a dependency-free Node script that
publishes all 7 packages for one release. `.github/workflows/release.yml`'s
`publish-npm` job (which `needs: build-release-assets`, so it reuses the
same cross-compiled binaries `upload-release-assets` attaches to the GitHub
release) extracts each of the 6 release archives to a raw binary and calls
this script to do the rest:

```
node scripts/publish.mjs --binaries-dir <dir> --tag <tag> [--dry-run] [--skip-staging] [--no-latest-follow] [--npm-root <dir>]
```

- `--binaries-dir <dir>` (required unless `--skip-staging`) - a flat
  directory of already-extracted Go binaries named
  `rhizome-mcp_<goos>_<goarch>` (`.exe` on windows), e.g.
  `rhizome-mcp_darwin_arm64`, `rhizome-mcp_windows_amd64.exe`. This script
  does not build binaries or know about release.yml's archive naming - the
  caller extracts archives into this layout first.
- `--tag <tag>` (required) - the git release tag, e.g. `v1.0.0-beta.3` or
  `v1.0.0`. Unlike the VS Code Marketplace pipeline, npm accepts semver
  prerelease suffixes directly, so the published version is the tag with
  its leading `v` stripped, verbatim - no odd/even remapping.
- `--dry-run` - runs `npm publish --dry-run` (packs and validates, never
  uploads) and only logs the `npm dist-tag add` calls it would otherwise
  make. Binary staging, `package.json` version stamping, and the read-only
  `npm view` checks (idempotency + "has a stable version shipped" below)
  still happen for real, since they're either local-disk writes or
  read-only registry reads.
- `--skip-staging` - skip copying binaries into each platform package's
  `bin/`; assumes they're already staged. Useful for re-running just the
  publish logic.
- `--no-latest-follow` - disables the "beta also moves `latest`" behavior
  below unconditionally. Manual escape hatch.
- `--npm-root <dir>` - override the directory containing the 7 package
  dirs (defaults to this `packages/npm/` directory).

Per package, the script: stages the platform's binary into `bin/`
(`chmod +x` on non-Windows), stamps `version` (and, for the main package,
its `optionalDependencies` pins on the 6 platform packages, which must move
in lockstep), skips publishing if that exact `<pkg>@<version>` already
exists on the registry (idempotent re-runs), and otherwise runs
`npm publish --provenance`. **Platform packages are published before the
main package**, so `optionalDependencies` are never left pointing at a
version that isn't on the registry yet.

### Dist-tag policy

- A stable tag (no `-beta.N` suffix) publishes with `--tag latest`
  (npm's own default, made explicit here).
- A beta tag (`-beta.N` suffix) publishes with `--tag beta`.

**Should `latest` ever point at a beta?** `npx rhizome-mcp` and
`npm install rhizome-mcp` both resolve the `latest` dist-tag by default.
All 7 packages currently have `latest` pointing at a non-functional `0.0.1`
placeholder that was published by hand before this pipeline existed. If
`latest` were left stuck there while every real release shipped under
`beta` (likely for a while - this project is pre-1.0), plain `npx
rhizome-mcp` would stay useless indefinitely.

So: **while no real stable version has ever been published, a beta publish
also repoints `latest` at that same version** (`npm dist-tag add
<pkg>@<version> latest`, run immediately after the `--tag beta` publish
succeeds). Once a real stable version ships for a package, this stops -
only stable publishes move `latest` from then on.

"Has a real stable version ever been published" is decided per package via
`npm view <pkg> versions --json`: any returned version that both (a) isn't
a `-beta.N` prerelease and (b) isn't the `0.0.1` bootstrap placeholder
counts as a real stable release having shipped. `0.0.1` is deliberately
excluded from that check - it predates this pipeline, ships no `bin/`
payload, and treating its mere presence as "stable already shipped" would
permanently disable the latest-follows-beta behavior this policy exists
for, even though no real release has gone out yet.

### Idempotency

Before publishing each package, the script runs
`npm view <pkg>@<version> version`. If that succeeds, the version is
already on the registry (e.g. a re-run after a partially-failed release
job) and the package is skipped with a log line, rather than letting
`npm publish` fail on a duplicate version.
