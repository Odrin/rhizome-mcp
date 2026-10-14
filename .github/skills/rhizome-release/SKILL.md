---
name: rhizome-release
description: "Create a rhizome-mcp release: write the dated CHANGELOG.md entry from commits since the previous tag, run the full verification suites, commit and push main, publish the GitHub release (which triggers the tag-driven release workflow), and verify the workflow run. Use whenever someone asks to cut, prepare, publish, or create a release or version (e.g. 'release v1.7.0'), or to generate release notes or a changelog for an upcoming version."
argument-hint: "Target version, e.g. v1.7.0 (derive from the changes if omitted: breaking = major, features = minor, fixes only = patch)."
---

# Rhizome release

Releases are published from `main`. Publishing a GitHub release creates the tag and fires `.github/workflows/release.yml` (`release: published`), which builds binaries and publishes npm, the MCP Registry, the VS Code Marketplace, and Open VSX. Do not push tags by hand and do not stamp versions in files: the workflow stamps `server.json`, the VS Code extension version, and (afterwards, on `main`) `plugins/rhizome-mcp/.claude-plugin/plugin.json`.

Track the release as a rhizome-mcp issue like any other work (see the `rhizome-task-workflow` skill): create a `task` titled `Publish vX.Y.Z release` with the `distribution` label, claim it with `CHANGELOG.md` as a reserved `file` resource, save a checkpoint after verification, and `finish_attempt` with the commit, release URL, and workflow run URL as artifacts.

## 1. Scope the release

```bash
git pull && git status --short --branch        # must be clean and not behind
git tag --sort=-version:refname | head -3      # previous tag
git log --oneline v<prev>..HEAD                # full range
git log --format='%h %s' v<prev>..HEAD | rg -v 'Update (unit|integration) coverage badge|stamp plugin manifest'
```

Ignore generated badge and plugin-stamp commits. Read the diffs of consequential commits (`git show --no-ext-diff --unified=8 <sha>`) instead of trusting subjects. Confirm the new tag and release do not exist yet:

```bash
git ls-remote --exit-code origin refs/tags/vX.Y.Z   # must fail
gh release view vX.Y.Z --repo Odrin/rhizome-mcp     # must fail
```

Pick the version: breaking contract change = major, new capability = minor, fixes/internal only = patch.

## 2. Write the CHANGELOG entry

Insert `## [X.Y.Z] - YYYY-MM-DD` (today) directly above the previous release in `CHANGELOG.md`. Follow Keep a Changelog sections in this order: `Added`, `Changed`, `Fixed`, `Removed`, `Internal`; omit empty ones.

- One bullet per behavior, not per commit: `- **Bold summary** — what changed, who is affected, observable effect and error codes.` Group related commits (e.g. several `perf(...)` commits become one bullet).
- Describe shipped behavior verified in the diff, not commit-message intent. Name tools, commands, flags, and error codes in backticks.
- Release-pipeline, CI, test-only, and coverage changes go under `Internal`.

## 3. Verify

Run each from its own directory; do not run these in parallel in one shared terminal (the working directory leaks between them).

```bash
cd <repo root> && go test ./...
cd <repo root> && go test -tags=integration ./...
cd <repo root>/packages/npm/rhizome-mcp && npm test      # one binary-dependent smoke test skips without a checked-in binary
cd <repo root>/editors/vscode && npm test                # compiles, lints, runs VS Code tests
cd <repo root> && git diff --check
```

Also assert the changelog heading exists exactly once and sits above the previous release. Fix failures before continuing; never release over a red suite.

## 4. Commit and push

```bash
git add CHANGELOG.md && git commit -m 'docs(changelog): prepare vX.Y.Z' && git push origin main
```

Only `CHANGELOG.md` should be in this commit. Confirm `git rev-parse HEAD` equals `git ls-remote origin refs/heads/main`.

## 5. Publish the GitHub release

Use the changelog section verbatim as the notes and target the pushed commit:

```bash
sha=$(git rev-parse HEAD)
notes=$(awk '/^## \[X\.Y\.Z\] - / { c=1; next } /^## \[/ { c=0 } c { print }' CHANGELOG.md)
GH_PAGER=cat gh release create vX.Y.Z --repo Odrin/rhizome-mcp --target "$sha" --title vX.Y.Z --notes "$notes" --latest
```

For a prerelease tag (`vX.Y.Z-beta.N`) use `--prerelease` instead of `--latest`; the extension then publishes on the pre-release channel (see CONTRIBUTING.md "Version policy").

## 6. Verify the release

- Release: published, not draft, `target_commitish` equals the pushed SHA, body matches the changelog.
- Tag `refs/tags/vX.Y.Z` points at the same SHA.
- Workflow: newest `release.yml` run with `event: release`, `head_branch: vX.Y.Z`, same `head_sha`; record its `html_url`. It starts `queued`/`in_progress`, and release assets appear only as it completes. If a publish job fails, the aggregate release-status job fails; recover with `workflow_dispatch` (`target: vscode` or `mcp-registry`) rather than re-releasing.

Use `GH_PAGER=cat` (or `--json` with `cat`) for `gh` commands; an interactive pager blocks the terminal.
