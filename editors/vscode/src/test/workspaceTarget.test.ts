import * as assert from 'assert';
import * as vscode from 'vscode';
import { resolveTargetWorkspaceFolder } from '../workspaceTarget';

/**
 * Integration test for `resolveTargetWorkspaceFolder` against the real
 * single-folder workspace this whole test run opens (see `.vscode-test.mjs`).
 *
 * Only the single-folder ("direct") path is exercised here: it's the one
 * case reachable without changing which folders are open or driving a real
 * `showQuickPick` prompt. Per the doc comment in
 * `./mcpServerProvider.test.ts`, changing the open folder count (to exercise
 * "no folders open" or "more than one folder open, prompt via QuickPick")
 * was observed to restart the extension host mid-test and hang the test
 * run, so those two branches are not covered here as live integration
 * tests. They're covered instead by the pure `selectTargetFolder` unit
 * tests in `../../test/commandTarget.test.js`, which exercise the
 * folder-count decision itself without needing a real workspace-folder
 * transition or a real QuickPick UI interaction.
 */
suite('resolveTargetWorkspaceFolder (real workspace integration)', () => {
  test('resolves directly to the sole open workspace folder, no prompt needed', async () => {
    const folders = vscode.workspace.workspaceFolders;
    assert.ok(folders && folders.length === 1, 'expected the test run to open exactly one workspace folder');

    const result = await resolveTargetWorkspaceFolder();
    assert.equal(result.kind, 'folder');
    if (result.kind === 'folder') {
      assert.equal(result.folder.uri.toString(), folders[0].uri.toString());
    }
  });
});
