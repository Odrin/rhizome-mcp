import { defineConfig } from '@vscode/test-cli';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// A single real folder, opened as the sole workspace folder for the whole
// test run. src/test/mcpServerProvider.test.ts toggles files inside it
// (.agent-tracker.json, .vscode/mcp.json) between tests rather than calling
// vscode.workspace.updateWorkspaceFolders(): per the `updateWorkspaceFolders`
// docs, transitioning into/out of a 0-or-1-folder workspace can terminate
// and restart the extension host mid-run, which killed the in-flight test
// promise/event subscription when tried. Keeping the folder set constant
// avoids that entirely.
const workspaceFolder = fs.mkdtempSync(path.join(os.tmpdir(), 'rhizome-mcp-vscode-test-workspace-'));

// Fresh per-run user-data-dir, for two reasons:
//   1. This extension's own worktree/repo path can be long enough that the
//      default `.vscode-test/user-data` dir overflows the ~103-char unix
//      domain socket path limit (observed on macOS when run from a deeply
//      nested git worktree path), causing VS Code to fail to start with
//      `EINVAL: invalid argument ... main.sock`. `os.tmpdir()` itself can
//      also be too long for this on macOS (it's a long per-user
//      `/var/folders/...` path), so this uses `/tmp` directly on unix,
//      which is short and always present there. Windows has no `/tmp`, and
//      Windows doesn't hit this unix-domain-socket path-length limit in the
//      first place, so it falls back to `os.tmpdir()` there instead.
//   2. A *fixed* user-data-dir reused across runs made VS Code's window
//      -restore feature reopen windows left over from earlier test runs
//      (pointing at their own now-deleted temporary workspace folders) in
//      addition to the current one — and each restored window
//      independently re-ran the whole test suite against the *current*
//      run's workspace folder, racing each other's file writes/removals
//      and producing spurious ENOENT failures. A fresh directory per run
//      means there's never any restorable window state to begin with.
const userDataTmpBase = process.platform === 'win32' ? os.tmpdir() : '/tmp';
const userDataDir = fs.mkdtempSync(path.join(userDataTmpBase, 'rhizome-mcp-vscode-test-user-data-'));

export default defineConfig({
  files: 'out/test/**/*.test.js',
  workspaceFolder,
  launchArgs: [`--user-data-dir=${userDataDir}`],
});
