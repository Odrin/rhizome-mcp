# Rhizome MCP

VS Code extension bundling the rhizome-mcp server as a native MCP provider.

See the [main repository](https://github.com/Odrin/rhizome-mcp) for full documentation.

## Platform packaging

Each published VSIX bundles the matching platform's `rhizome-mcp` Go binary at
`bin/rhizome-mcp[.exe]`. `scripts/package-platforms.mjs` builds these
platform-specific VSIXes; see that file's header comment for the full input
contract (the exact binary filenames it expects) and the Go-binary-to-target
mapping (6 binaries -> 8 Marketplace targets, since the static Linux binary
also serves Alpine as-is).

Run `npm run package:local` to cross-compile the current platform's binary
and produce a single installable `.vsix` for manual testing.

### Version policy

The VS Code Marketplace requires a plain `major.minor.patch` version, but
this project's git tags carry a prerelease suffix (`v1.0.0-beta.3`). Versions
are derived from the git tag as follows:

- **Beta tag** (`vMAJOR.MINOR.PATCH-beta.N`): the extension version is
  `MAJOR.(MINOR*2+1).(PATCH*1000+N)` — the minor is forced odd, which is the
  Marketplace's own documented convention for marking a build as pre-release,
  and the build is published with `vsce package --pre-release`. For example,
  tag `v1.0.0-beta.3` becomes extension version `1.1.3`.
- **Stable tag** (`vMAJOR.MINOR.PATCH`, no `-beta.N`): the extension version
  is `MAJOR.MINOR.PATCH` verbatim, published without `--pre-release`. Once
  the server ships its first stable release, this extension's version locks
  step with the server's own version from then on — no more remapping.

The tag is resolved from `--tag`, then the `RHIZOME_RELEASE_TAG` environment
variable, then `git describe --tags --abbrev=0`. See
`scripts/package-platforms.mjs` for the exact implementation.
