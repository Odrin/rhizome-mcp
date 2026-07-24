# Contributing to rhizome-mcp

Thank you for your interest in contributing. This document explains how to build, test, and submit changes.

## Build

Build the native binary without CGO:

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
```

No external dependencies, Docker, or services are required. The binary is self-contained and portable.

## Test

Run the full test suite:

```bash
go test ./...
```

The real-process MCP workflow tests are isolated behind the `integration` build tag. These build a temporary server binary, initialize a fresh database, and test the full MCP lifecycle. Most of them live in the `integration` package, split into files by tested area (init, smoke/issue workflow, review, logical project round-trip, HTTP transport, connect, board); one test that needs unexported package-main internals stays in `integration_test.go` at the repository root:

```bash
go test -tags=integration ./...
```

Both test commands should pass before submitting a pull request.

## Code style

- Follow the conventions documented in [AGENTS.md](AGENTS.md) and [AGENT_BRIEF.md](AGENT_BRIEF.md)
- Business logic belongs in application/domain layers; keep adapters (MCP, CLI, storage) thin
- Keep writes short and atomic; use optimistic concurrency for mutations
- Preserve all history; avoid deleting issues or events
- Inject time and dependencies; keep functions deterministic and testable

## Repository tracking

This repository uses `rhizome-mcp` itself for issue tracking and backlog management. Before starting work:

1. Check the configured MCP project for related issues and planning graphs
2. Claim an executable issue through MCP before editing
3. Use attempt notes to save checkpoints and handoffs
4. Call `finish_attempt` on completion with verification summary
5. Update issue status, blockers, and relations through MCP (not Markdown)

See [AGENTS.md](AGENTS.md) for the full workflow.

## Submitting changes

1. Create a feature branch from `main`
2. Make your changes and ensure tests pass
3. Keep commits focused and descriptive
4. Open a pull request with a clear summary of changes and testing performed
5. Link to the related MCP issue(s) in the PR description

## VS Code extension

The VS Code extension lives in `editors/vscode/` as a self-contained npm project.

```bash
cd editors/vscode
npm install
npm run watch      # esbuild in watch mode
```

Press F5 from that folder to launch an Extension Development Host. Run `npm run lint`, `npm run typecheck`, and `npm test` before submitting changes; `npm run package` produces an installable `.vsix`.

### Platform packaging

Published VSIXes bundle a platform-specific `rhizome-mcp` Go binary; see
`editors/vscode/scripts/package-platforms.mjs`'s header comment for the exact
input contract (binary naming) and the Go-binary-to-Marketplace-target
mapping. Run `npm run package:local` from `editors/vscode/` to cross-compile
the current platform's binary and produce one installable `.vsix` for manual
testing.

### Version policy

The Marketplace requires a plain `major.minor.patch`
version, but this project's git tags carry a prerelease suffix
(`v1.0.0-beta.N`). The packaging script derives the extension's version from
the git tag:

- Beta tag `vMAJOR.MINOR.PATCH-beta.N` -> extension version
  `MAJOR.(MINOR*2+1).(PATCH*1000+N)`, published with `vsce package
  --pre-release`. Forcing the minor odd follows the Marketplace's documented
  convention for distinguishing its pre-release channel from stable.
- Stable tag `vMAJOR.MINOR.PATCH` (no `-beta.N`) -> extension version
  `MAJOR.MINOR.PATCH` verbatim, published without `--pre-release`. Once the
  server reaches a stable release, the extension's version locks step with
  the server's own version from then on; there is no more odd/even
  remapping after that point.

## Documentation

- Specification changes go in the modular docs/ files (01-09), not README.md
- Product and technical decisions are recorded as MCP decisions (durable, versioned)
- Active backlog and implementation status live in MCP issues, not Markdown

See [SPEC.md](SPEC.md) for the specification index and [docs/06-deferred-and-open.md](docs/06-deferred-and-open.md) for known trade-offs and future work.
