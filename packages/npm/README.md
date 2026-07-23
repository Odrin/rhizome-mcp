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
release binaries (built from `.github/workflows/release.yml`'s existing
cross-compile matrix) and publishing all 7 packages to npm in lockstep is
the responsibility of a later release-automation issue - not this one.
