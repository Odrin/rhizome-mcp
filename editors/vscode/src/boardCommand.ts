/**
 * Thin `vscode`-facing glue registering the `rhizome-mcp.showBoard` command
 * ("Rhizome: Open Status Board" in the Command Palette): resolves the
 * target workspace folder (see `./workspaceTarget.ts`), spawns
 * `<binary> board --serve --http-address 127.0.0.1:0` with `cwd` set to that
 * folder, reads the canonical loopback URL from stdout, and opens it in the
 * OS default browser while keeping the child process alive for the extension
 * session.
 */

import { spawn } from 'node:child_process';
import * as vscode from 'vscode';
import { getLastResolution, getOutputChannel, showResolutionFailure } from './activation';
import { extractBoardServeURL } from './commandTarget';
import { resolveTargetWorkspaceFolder } from './workspaceTarget';

interface BoardProcessState {
  child: ReturnType<typeof spawn>;
  url: string | null;
  disposed: boolean;
}

let activeBoardProcess: BoardProcessState | undefined;

/** Runs `<binaryPath> board --serve --http-address 127.0.0.1:0` in `cwd`, streaming stderr and the canonical URL line from stdout into `outputChannel`. */
function runBoardProcess(
  binaryPath: string,
  cwd: string,
  outputChannel: vscode.OutputChannel,
): Promise<BoardProcessState> {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath, ['board', '--serve', '--http-address', '127.0.0.1:0'], { cwd, shell: false });
    const state: BoardProcessState = { child, url: null, disposed: false };
    let bufferedLine = '';
    let settled = false;

    const finishWithError = (message: string): void => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error(message));
    };

    const resolveOnce = (resolvedState: BoardProcessState): void => {
      if (settled) {
        return;
      }
      settled = true;
      resolve(resolvedState);
    };

    child.stdout?.on('data', (chunk: Buffer | string) => {
      const text = chunk.toString();
      const parts = `${bufferedLine}${text}`.split(/\r?\n/);
      bufferedLine = parts.pop() ?? '';

      for (const line of parts) {
        const trimmedLine = line.trim();
        if (trimmedLine === '') {
          continue;
        }
        const url = extractBoardServeURL(trimmedLine);
        if (url !== null) {
          state.url = url;
          resolveOnce(state);
          return;
        }
        outputChannel.appendLine(trimmedLine);
      }
    });

    child.stderr?.on('data', (chunk: Buffer | string) => {
      outputChannel.append(chunk.toString());
    });
    child.once('error', (err) => finishWithError(err.message));
    child.once('close', (code) => {
      if (settled) {
        return;
      }

      if (bufferedLine !== '') {
        const url = extractBoardServeURL(bufferedLine);
        if (url !== null) {
          state.url = url;
          resolveOnce(state);
          return;
        }
        outputChannel.appendLine(bufferedLine.trim());
      }

      if (!state.disposed && state.url === null) {
        finishWithError(`board process exited before reporting a startup URL (code ${code ?? 'null'})`);
      }
    });
  });
}

async function openBoard(url: string): Promise<void> {
  await vscode.env.openExternal(vscode.Uri.parse(url));
}

function stopActiveBoardProcess(): void {
  if (activeBoardProcess === undefined) {
    return;
  }
  activeBoardProcess.disposed = true;
  if (!activeBoardProcess.child.killed) {
    activeBoardProcess.child.kill('SIGTERM');
  }
  activeBoardProcess = undefined;
}

/** Registers the `rhizome-mcp.showBoard` command. */
export function registerShowBoardCommand(): vscode.Disposable {
  return vscode.commands.registerCommand('rhizome-mcp.showBoard', async () => {
    stopActiveBoardProcess();

    const target = await resolveTargetWorkspaceFolder();

    if (target.kind === 'no-folders-open') {
      await vscode.window.showErrorMessage('Open a folder first to view the Rhizome status board.');
      return;
    }
    if (target.kind === 'cancelled') {
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
    outputChannel.appendLine(`[info] Running "rhizome-mcp board --serve" in ${folder.uri.fsPath}`);

    try {
      const state = await runBoardProcess(resolution.binaryPath, folder.uri.fsPath, outputChannel);
      activeBoardProcess = state;
      if (state.url !== null) {
        await openBoard(state.url);
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      outputChannel.appendLine(`[error] failed to run "rhizome-mcp board --serve": ${message}`);
      await vscode.window.showErrorMessage(
        'Failed to run rhizome-mcp board. See the "Rhizome MCP" output channel for details.',
      );
    }
  });
}

export function disposeBoardProcess(): void {
  stopActiveBoardProcess();
}
