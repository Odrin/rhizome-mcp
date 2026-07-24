/**
 * Thin `vscode`-facing glue that resolves which real workspace folder a
 * command should run against, delegating the folder-count decision to the
 * pure `selectTargetFolder` in `./commandTarget.ts` and only prompting via
 * `vscode.window.showQuickPick` in the ambiguous (multi-root) case.
 *
 * Shared by `./initCommand.ts` and `./boardCommand.ts` — each interprets
 * `no-folders-open` and `cancelled` slightly differently (see their own
 * doc comments), so this only reports what happened rather than deciding
 * what UX to show.
 */

import * as vscode from 'vscode';
import { selectTargetFolder } from './commandTarget';

export type WorkspaceTargetResult =
  | { kind: 'folder'; folder: vscode.WorkspaceFolder }
  | { kind: 'no-folders-open' }
  | { kind: 'cancelled' };

/**
 * Resolves the target workspace folder for a command invocation:
 *   - no folders open → `{ kind: 'no-folders-open' }`
 *   - exactly one folder open → `{ kind: 'folder', folder }` directly, no prompt
 *   - more than one folder open → prompts with `showQuickPick`; a folder
 *     pick becomes `{ kind: 'folder', folder }`, cancelling (Escape / no
 *     selection) becomes `{ kind: 'cancelled' }`
 */
export async function resolveTargetWorkspaceFolder(): Promise<WorkspaceTargetResult> {
  const folders = vscode.workspace.workspaceFolders ?? [];
  const selection = selectTargetFolder(folders);

  if (selection.kind === 'none') {
    return { kind: 'no-folders-open' };
  }
  if (selection.kind === 'direct') {
    return { kind: 'folder', folder: selection.folder };
  }

  const picked = await vscode.window.showQuickPick(
    selection.folders.map((folder) => ({ label: folder.name, folder })),
    { placeHolder: 'Select a workspace folder' },
  );

  if (!picked) {
    return { kind: 'cancelled' };
  }
  return { kind: 'folder', folder: picked.folder };
}
