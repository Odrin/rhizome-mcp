import * as assert from 'assert';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';
import { createRhizomeMcpServerProvider, type RhizomeMcpServerProvider } from '../mcpServerProvider';

/**
 * Integration tests for `createRhizomeMcpServerProvider` against a real
 * `vscode.workspace` — a real temporary folder on disk (created by
 * `.vscode-test.mjs` and opened as the sole workspace folder for the whole
 * test run), queried through the provider's actual `provideMcpServerDefinitions`.
 *
 * Each test toggles files *inside* that one folder (`.agent-tracker.json`,
 * `.vscode/mcp.json`) rather than changing which folders are open via
 * `vscode.workspace.updateWorkspaceFolders`: per that API's own docs,
 * transitioning a workspace into/out of a 0-or-1-folder state can terminate
 * and restart the extension host mid-operation, which was observed to hang
 * the test run (the in-flight `onDidChangeWorkspaceFolders` subscription
 * never fired because the host restarted before it could resolve). Keeping
 * the workspace folder set constant sidesteps that entirely.
 *
 * A true multi-root scenario (two folders open simultaneously) was
 * attempted the same way and hit the same extension-host-restart hang when
 * going from single- to multi-folder, so it's not covered here as a live
 * VS Code integration test; the label rule for the multi-root case (and the
 * single-root case) is covered directly by the pure unit tests in
 * `../../test/mcpProvider.test.js` (`computeServerLabel`), which don't
 * depend on any real workspace-folder transition.
 */

const FAKE_BINARY_PATH = '/fake/rhizome-mcp';
const FAKE_VERSION = '1.2.3';

function requireSoleWorkspaceFolder(): vscode.WorkspaceFolder {
  const folders = vscode.workspace.workspaceFolders;
  assert.ok(folders && folders.length === 1, 'expected the test run to open exactly one workspace folder');
  return folders[0];
}

function trackerPath(folder: vscode.WorkspaceFolder): string {
  return path.join(folder.uri.fsPath, '.agent-tracker.json');
}

function mcpJsonDir(folder: vscode.WorkspaceFolder): string {
  return path.join(folder.uri.fsPath, '.vscode');
}

function mcpJsonPath(folder: vscode.WorkspaceFolder): string {
  return path.join(mcpJsonDir(folder), 'mcp.json');
}

function writeTracker(folder: vscode.WorkspaceFolder): void {
  fs.writeFileSync(trackerPath(folder), JSON.stringify({ version: 1, project_id: 'test' }));
}

function removeTracker(folder: vscode.WorkspaceFolder): void {
  fs.rmSync(trackerPath(folder), { force: true });
}

function writeMcpJsonWithRhizome(folder: vscode.WorkspaceFolder): void {
  fs.mkdirSync(mcpJsonDir(folder), { recursive: true });
  fs.writeFileSync(
    mcpJsonPath(folder),
    JSON.stringify({
      servers: {
        'rhizome-mcp': { type: 'stdio', command: '/somewhere/rhizome-mcp', args: ['serve'] },
      },
    }),
  );
}

function removeMcpJson(folder: vscode.WorkspaceFolder): void {
  fs.rmSync(mcpJsonPath(folder), { force: true });
}

/** Calls provideMcpServerDefinitions with a throwaway cancellation token and normalizes the ProviderResult to a plain array. */
async function collectDefinitions(provider: RhizomeMcpServerProvider): Promise<vscode.McpStdioServerDefinition[]> {
  const tokenSource = new vscode.CancellationTokenSource();
  try {
    const result = await provider.provideMcpServerDefinitions(tokenSource.token);
    return result ?? [];
  } finally {
    tokenSource.dispose();
  }
}

suite('createRhizomeMcpServerProvider (real workspace integration)', () => {
  let folder: vscode.WorkspaceFolder;
  let provider: RhizomeMcpServerProvider | undefined;
  let outputChannel: vscode.OutputChannel;

  suiteSetup(() => {
    folder = requireSoleWorkspaceFolder();
    outputChannel = vscode.window.createOutputChannel('Rhizome MCP Test');
  });

  suiteTeardown(() => {
    outputChannel.dispose();
  });

  teardown(() => {
    provider?.dispose();
    provider = undefined;

    removeTracker(folder);
    removeMcpJson(folder);
  });

  test('returns a definition with the right command/args/cwd/version/label for a single initialized folder', async () => {
    writeTracker(folder);

    provider = createRhizomeMcpServerProvider({ binaryPath: FAKE_BINARY_PATH, version: FAKE_VERSION }, outputChannel);

    const definitions = await collectDefinitions(provider);
    assert.equal(definitions.length, 1, 'expected exactly one MCP server definition');

    const [definition] = definitions;
    assert.equal(definition.label, 'rhizome');
    assert.equal(definition.command, FAKE_BINARY_PATH);
    assert.deepEqual(definition.args, ['serve']);
    assert.equal(definition.version, FAKE_VERSION);
    assert.ok(definition.cwd, 'expected cwd to be set');
    assert.equal(path.resolve(definition.cwd!.fsPath), path.resolve(folder.uri.fsPath));
  });

  test('yields no definition for a workspace folder without .agent-tracker.json', async () => {
    // Deliberately do NOT write .agent-tracker.json.

    provider = createRhizomeMcpServerProvider({ binaryPath: FAKE_BINARY_PATH, version: FAKE_VERSION }, outputChannel);

    const definitions = await collectDefinitions(provider);
    assert.equal(definitions.length, 0, 'expected no definitions for an uninitialized folder');
  });

  test('yields no definition when .vscode/mcp.json already registers a rhizome-mcp server (duplicate guard)', async () => {
    writeTracker(folder);
    writeMcpJsonWithRhizome(folder);

    provider = createRhizomeMcpServerProvider({ binaryPath: FAKE_BINARY_PATH, version: FAKE_VERSION }, outputChannel);

    const definitions = await collectDefinitions(provider);
    assert.equal(definitions.length, 0, 'expected the duplicate guard to suppress the definition');
  });
});
