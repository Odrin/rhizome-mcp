/**
 * Thin `vscode`-facing glue registering the `rhizome-mcp.showBoard` command
 * ("Rhizome: Open Status Board" in the Command Palette): resolves the
 * target workspace folder (see `./workspaceTarget.ts`), spawns
 * `<binary> board --output <tempFile>` with `cwd` set to that folder, and
 * on success opens the generated HTML — preferring VS Code's built-in
 * Simple Browser, falling back to the OS default browser if that built-in
 * isn't available in this VS Code distribution.
 */

import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import * as os from 'node:os';
import * as vscode from 'vscode';
import { getLastResolution, getOutputChannel, showResolutionFailure } from './activation';
import { generateBoardTempFilePath } from './commandTarget';
import { resolveTargetWorkspaceFolder } from './workspaceTarget';

/** Runs `<binaryPath> board --output <outputPath>` in `cwd`, streaming stderr into `outputChannel` line-by-line as it arrives. Resolves with the process's exit code (or `null` if it terminated via signal); rejects if the process could not be spawned at all. */
function runBoardProcess(
  binaryPath: string,
  cwd: string,
  outputPath: string,
  outputChannel: vscode.OutputChannel,
): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath, ['board', '--output', outputPath], { cwd, shell: false });

    child.stderr?.on('data', (chunk: Buffer | string) => {
      outputChannel.append(chunk.toString());
    });
    child.once('error', (err) => reject(err));
    child.once('close', (code) => resolve(code));
  });
}

/** Opens the generated board HTML for the user: Simple Browser first (keeps the view inside the editor), falling back to the OS default browser if the Simple Browser built-in isn't available in this VS Code distribution. */
async function openBoard(fileUri: vscode.Uri): Promise<void> {
  try {
    await vscode.commands.executeCommand('simpleBrowser.show', fileUri.toString());
  } catch {
    await vscode.env.openExternal(fileUri);
  }
}

/** Registers the `rhizome-mcp.showBoard` command. */
export function registerShowBoardCommand(): vscode.Disposable {
  return vscode.commands.registerCommand('rhizome-mcp.showBoard', async () => {
    const target = await resolveTargetWorkspaceFolder();

    if (target.kind === 'no-folders-open') {
      await vscode.window.showErrorMessage('Open a folder first to view the Rhizome status board.');
      return;
    }
    if (target.kind === 'cancelled') {
      // Unlike init, there's no meaningful project context to fall back to
      // quietly here — surface it as an error.
      await vscode.window.showErrorMessage('Select a workspace folder to view the Rhizome status board.');
      return;
    }

    const folder = target.folder;

    const resolution = getLastResolution();
    if (!resolution || resolution.binaryPath === null) {
      await showResolutionFailure();
      return;
    }

    const outputChannel = getOutputChannel();
    const outputPath = generateBoardTempFilePath(os.tmpdir(), randomUUID());
    outputChannel.appendLine(`[info] Running "rhizome-mcp board" in ${folder.uri.fsPath}`);

    try {
      const code = await runBoardProcess(resolution.binaryPath, folder.uri.fsPath, outputPath, outputChannel);
      if (code === 0) {
        await openBoard(vscode.Uri.file(outputPath));
      } else {
        outputChannel.appendLine(`[error] "rhizome-mcp board" exited with code ${code}`);
        await vscode.window.showErrorMessage(
          `rhizome-mcp board failed (exit code ${code}). See the "Rhizome MCP" output channel for details.`,
        );
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      outputChannel.appendLine(`[error] failed to run "rhizome-mcp board": ${message}`);
      await vscode.window.showErrorMessage(
        'Failed to run rhizome-mcp board. See the "Rhizome MCP" output channel for details.',
      );
    }
  });
}
