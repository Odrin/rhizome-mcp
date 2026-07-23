/**
 * Thin `vscode`-facing glue that registers this extension's
 * `vscode.lm.registerMcpServerDefinitionProvider("rhizome-mcp.servers", ...)`
 * contribution (the provider id is declared in package.json's
 * `contributes.mcpServerDefinitionProviders`).
 *
 * Enumerates real `vscode.workspace.workspaceFolders` and reads real files
 * from disk, but delegates every decision that doesn't need live VS Code
 * state — the label rule, and the `.vscode/mcp.json` duplicate-detection
 * logic — to the pure functions in `./mcpProvider.ts`, mirroring the
 * `binaryResolver.ts` (pure) / `activation.ts` (glue) split used elsewhere
 * in this extension.
 */

import * as fs from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';
import { computeServerLabel, hasRhizomeServerEntry, TRACKER_FILENAME } from './mcpProvider';

/** The subset of a resolved-and-validated binary this provider needs. */
export interface RhizomeBinaryInfo {
  binaryPath: string;
  version: string | null;
}

/**
 * A `McpServerDefinitionProvider` plus a `dispose()` for its internal file
 * watchers and listeners. Exported (rather than only reachable indirectly
 * through `vscode.lm`) so tests can construct one directly against a real
 * temporary workspace folder and call `provideMcpServerDefinitions` without
 * going through the language-model server registry.
 */
export interface RhizomeMcpServerProvider
  extends vscode.McpServerDefinitionProvider<vscode.McpStdioServerDefinition> {
  dispose(): void;
}

function mcpJsonPath(folder: vscode.WorkspaceFolder): string {
  return path.join(folder.uri.fsPath, '.vscode', 'mcp.json');
}

function trackerPath(folder: vscode.WorkspaceFolder): string {
  return path.join(folder.uri.fsPath, TRACKER_FILENAME);
}

/**
 * Builds the provider instance. Split out from `registerRhizomeMcpServerProvider`
 * so tests can construct and query one directly, and so the watcher/listener
 * wiring can be unit-exercised (via its returned `dispose()`) without a real
 * `vscode.lm` registration.
 */
export function createRhizomeMcpServerProvider(
  binaryInfo: RhizomeBinaryInfo,
  outputChannel: vscode.OutputChannel,
): RhizomeMcpServerProvider {
  const changeEmitter = new vscode.EventEmitter<void>();
  // Logs the duplicate-guard explanation once per folder per session, not once per check.
  const loggedDuplicateFolders = new Set<string>();
  let trackerWatchers: vscode.FileSystemWatcher[] = [];

  function disposeTrackerWatchers(): void {
    for (const watcher of trackerWatchers) {
      watcher.dispose();
    }
    trackerWatchers = [];
  }

  /** (Re)builds one file watcher per current workspace folder, watching only that folder's root-level tracker file. */
  function rebuildTrackerWatchers(): void {
    disposeTrackerWatchers();
    for (const folder of vscode.workspace.workspaceFolders ?? []) {
      const pattern = new vscode.RelativePattern(folder, TRACKER_FILENAME);
      // ignoreCreateEvents=false, ignoreChangeEvents=true, ignoreDeleteEvents=false:
      // only existence (create/delete) toggles the "is this folder initialized" signal.
      const watcher = vscode.workspace.createFileSystemWatcher(pattern, false, true, false);
      watcher.onDidCreate(() => changeEmitter.fire());
      watcher.onDidDelete(() => changeEmitter.fire());
      trackerWatchers.push(watcher);
    }
  }

  rebuildTrackerWatchers();

  const foldersSubscription = vscode.workspace.onDidChangeWorkspaceFolders(() => {
    rebuildTrackerWatchers();
    changeEmitter.fire();
  });

  function folderHasDuplicateRhizomeServer(folder: vscode.WorkspaceFolder): boolean {
    const mcpJsonFile = mcpJsonPath(folder);
    if (!fs.existsSync(mcpJsonFile)) {
      return false;
    }

    let raw: string;
    try {
      raw = fs.readFileSync(mcpJsonFile, 'utf8');
    } catch {
      // Unreadable despite existsSync succeeding (race, permissions, ...) — don't treat as covered.
      return false;
    }

    const hasEntry = hasRhizomeServerEntry(raw);
    if (hasEntry && !loggedDuplicateFolders.has(folder.uri.toString())) {
      loggedDuplicateFolders.add(folder.uri.toString());
      outputChannel.appendLine(
        `[info] Skipping MCP server registration for workspace folder "${folder.name}": ` +
          '.vscode/mcp.json already registers a "rhizome-mcp" server, so contributing another ' +
          'one here would duplicate it.',
      );
    }
    return hasEntry;
  }

  function provideMcpServerDefinitions(): vscode.McpStdioServerDefinition[] {
    const folders = vscode.workspace.workspaceFolders ?? [];
    const definitions: vscode.McpStdioServerDefinition[] = [];

    for (const folder of folders) {
      if (!fs.existsSync(trackerPath(folder))) {
        continue;
      }
      if (folderHasDuplicateRhizomeServer(folder)) {
        continue;
      }

      const label = computeServerLabel(folder.name, folders.length);
      const definition = new vscode.McpStdioServerDefinition(
        label,
        binaryInfo.binaryPath,
        ['serve'],
        undefined,
        binaryInfo.version ?? undefined,
      );
      definition.cwd = folder.uri;
      definitions.push(definition);
    }

    return definitions;
  }

  return {
    onDidChangeMcpServerDefinitions: changeEmitter.event,
    provideMcpServerDefinitions,
    resolveMcpServerDefinition(definition) {
      // No auth or other interactive step needed — the binary was already
      // resolved and version-validated at activation time, so the
      // definition can be used as-is.
      return definition;
    },
    dispose(): void {
      disposeTrackerWatchers();
      foldersSubscription.dispose();
      changeEmitter.dispose();
    },
  };
}

/**
 * Registers the `rhizome-mcp.servers` MCP server definition provider
 * (id declared in package.json's `contributes.mcpServerDefinitionProviders`)
 * and wires its disposal into `context.subscriptions`.
 *
 * No explicit workspace-trust check appears here or anywhere in this
 * provider: package.json declares `capabilities.untrustedWorkspaces:
 * { supported: false }`, and per VS Code's own extension-manifest contract
 * for that field (see `@types/vscode`'s docs for
 * `Extension.packageJSON`/the manifest `capabilities.untrustedWorkspaces`
 * field, and VS Code's public "Workspace Trust extension guide") a
 * `supported: false` extension is never activated at all in an untrusted
 * workspace or window — the editor enforces this itself before `activate()`
 * ever runs, so this provider can assume it always executes in a trusted
 * workspace.
 */
export function registerRhizomeMcpServerProvider(
  context: vscode.ExtensionContext,
  binaryInfo: RhizomeBinaryInfo,
  outputChannel: vscode.OutputChannel,
): RhizomeMcpServerProvider {
  const provider = createRhizomeMcpServerProvider(binaryInfo, outputChannel);
  const registration = vscode.lm.registerMcpServerDefinitionProvider('rhizome-mcp.servers', provider);
  context.subscriptions.push(registration, provider);
  return provider;
}
