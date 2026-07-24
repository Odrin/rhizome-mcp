/**
 * Pure, dependency-free logic backing the `rhizome-mcp.init` and
 * `rhizome-mcp.showBoard` commands:
 *
 *   - which workspace folder a command should run against, given the
 *     current set of open folders (no folders / exactly one / more than
 *     one), and
 *   - where the tracker marker file and a generated board HTML file should
 *     live, given a folder root / temp directory.
 *
 * Nothing in this file imports `vscode`, touches the real filesystem, or
 * spawns anything, so it can be exercised with plain `node --test` (see
 * `../test/commandTarget.test.js`) without booting VS Code. The thin glue
 * that supplies real `vscode.workspace` state, prompts via
 * `vscode.window.showQuickPick`, and spawns the CLI lives in
 * `./workspaceTarget.ts`, `./initCommand.ts`, and `./boardCommand.ts`.
 */

import * as path from 'node:path';

/** Name of the marker file `rhizome-mcp init` writes at a workspace/repo root. Mirrors `./mcpProvider.ts`'s constant of the same name/value. */
export const TRACKER_FILENAME = '.agent-tracker.json';

/**
 * The outcome of deciding which workspace folder a command should target,
 * given the current list of open workspace folders:
 *
 *   - `none`: zero folders open — there's nothing to target.
 *   - `direct`: exactly one folder open — use it, no picker needed.
 *   - `ambiguous`: more than one folder open — the caller must prompt
 *     (e.g. via `vscode.window.showQuickPick`) to disambiguate.
 *
 * Generic over the folder type so this stays independent of `vscode.WorkspaceFolder`.
 */
export type TargetFolderSelection<T> =
  | { kind: 'none' }
  | { kind: 'direct'; folder: T }
  | { kind: 'ambiguous'; folders: T[] };

/** Decides which of the three folder-count cases applies. Does not itself prompt or pick — that's the caller's job when `kind === 'ambiguous'`. */
export function selectTargetFolder<T>(folders: readonly T[]): TargetFolderSelection<T> {
  if (folders.length === 0) {
    return { kind: 'none' };
  }
  if (folders.length === 1) {
    return { kind: 'direct', folder: folders[0] };
  }
  return { kind: 'ambiguous', folders: [...folders] };
}

/** Absolute path to the tracker marker file at the root of `folderRoot`. */
export function trackerPathFor(folderRoot: string): string {
  return path.join(folderRoot, TRACKER_FILENAME);
}

/** Builds a per-invocation temp file path for a generated board HTML file, given a temp directory and a caller-supplied unique id (e.g. `crypto.randomUUID()`). Keeping the id injected (rather than generated in here) is what keeps this function pure and testable. */
export function generateBoardTempFilePath(tmpDir: string, uniqueId: string): string {
  return path.join(tmpDir, `rhizome-board-${uniqueId}.html`);
}
