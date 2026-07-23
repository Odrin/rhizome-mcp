/**
 * Thin `vscode`-facing glue registering the `rhizome-mcp.init` command
 * ("Rhizome: Initialize Project" in the Command Palette): resolves the
 * target workspace folder (see `./workspaceTarget.ts`), guards against
 * re-initializing an already-initialized folder (`rhizome-mcp init` itself
 * errors in that case — see `../../internal/projectconfig/projectconfig.go`'s
 * `Initialize`), spawns `<binary> init` with `cwd` set to that folder, and
 * on success asks the MCP server definition provider to `refresh()` so the
 * newly initialized project's server shows up immediately.
 */

import { spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as vscode from 'vscode';
import { getLastResolution, getOutputChannel, showResolutionFailure } from './activation';
import { trackerPathFor } from './commandTarget';
import type { RhizomeMcpServerProvider } from './mcpServerProvider';
import { resolveTargetWorkspaceFolder } from './workspaceTarget';

/** Runs `<binaryPath> init` in `cwd`, streaming stderr into `outputChannel` line-by-line as it arrives. Resolves with the process's exit code (or `null` if it terminated via signal); rejects if the process could not be spawned at all. */
function runInitProcess(binaryPath: string, cwd: string, outputChannel: vscode.OutputChannel): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath, ['init'], { cwd, shell: false });

    child.stderr?.on('data', (chunk: Buffer | string) => {
      outputChannel.append(chunk.toString());
    });
    child.once('error', (err) => reject(err));
    child.once('close', (code) => resolve(code));
  });
}

/**
 * Registers the `rhizome-mcp.init` command. `getProvider` is a callback
 * (rather than a direct reference) because the MCP server definition
 * provider may not exist yet at command-registration time — it's only
 * registered once binary resolution succeeds, and can change identity
 * across a full extension reactivation — so this always reads the current
 * value at invocation time.
 */
export function registerInitCommand(
  getProvider: () => RhizomeMcpServerProvider | undefined,
): vscode.Disposable {
  return vscode.commands.registerCommand('rhizome-mcp.init', async () => {
    const target = await resolveTargetWorkspaceFolder();

    if (target.kind === 'no-folders-open') {
      await vscode.window.showErrorMessage('Open a folder first to initialize a Rhizome project.');
      return;
    }
    if (target.kind === 'cancelled') {
      // User dismissed the folder picker — a quiet no-op, not an error.
      return;
    }

    const folder = target.folder;
    const trackerPath = trackerPathFor(folder.uri.fsPath);
    if (fs.existsSync(trackerPath)) {
      await vscode.window.showInformationMessage('This folder is already initialized.');
      return;
    }

    const resolution = getLastResolution();
    if (!resolution || resolution.binaryPath === null) {
      await showResolutionFailure();
      return;
    }

    const outputChannel = getOutputChannel();
    outputChannel.appendLine(`[info] Running "rhizome-mcp init" in ${folder.uri.fsPath}`);

    try {
      const code = await runInitProcess(resolution.binaryPath, folder.uri.fsPath, outputChannel);
      if (code === 0) {
        getProvider()?.refresh();
        await vscode.window.showInformationMessage('Rhizome project initialized.');
      } else {
        outputChannel.appendLine(`[error] "rhizome-mcp init" exited with code ${code}`);
        await vscode.window.showErrorMessage(
          `rhizome-mcp init failed (exit code ${code}). See the "Rhizome MCP" output channel for details.`,
        );
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      outputChannel.appendLine(`[error] failed to run "rhizome-mcp init": ${message}`);
      await vscode.window.showErrorMessage(
        'Failed to run rhizome-mcp init. See the "Rhizome MCP" output channel for details.',
      );
    }
  });
}
