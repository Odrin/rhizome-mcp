/**
 * Thin `vscode`-facing glue registering the `rhizome-mcp.showBoard` command
 * ("Rhizome: Open Status Board" in the Command Palette): resolves the
 * target workspace folder (see `./workspaceTarget.ts`), spawns
 * `<binary> board --serve --http-address 127.0.0.1:0` with `cwd` set to that
 * folder, reads the canonical loopback URL from stdout, and opens it in the
 * OS default browser while keeping the child process alive for the extension
 * session.
 */

import { spawn, type ChildProcess } from 'node:child_process';
import * as vscode from 'vscode';
import { getLastResolution, getOutputChannel, showResolutionFailure } from './activation';
import { extractBoardServeURL } from './commandTarget';
import { resolveTargetWorkspaceFolder } from './workspaceTarget';

interface BoardProcessState {
  generation: number;
  child: ChildProcess;
  url: string | null;
  disposed: boolean;
  ready: Promise<void>;
}

export interface ShowBoardCommandDeps {
  resolveTargetWorkspaceFolder: typeof resolveTargetWorkspaceFolder;
  getLastResolution: typeof getLastResolution;
  getOutputChannel: typeof getOutputChannel;
  showResolutionFailure: typeof showResolutionFailure;
  openExternal: typeof vscode.env.openExternal;
  showErrorMessage: typeof vscode.window.showErrorMessage;
  spawn: typeof spawn;
  extractBoardServeURL: typeof extractBoardServeURL;
}

let activeBoardProcess: BoardProcessState | undefined;
let activeBoardGeneration = 0;

export function resetBoardCommandStateForTests(): void {
  activeBoardProcess = undefined;
  activeBoardGeneration = 0;
}

function isCurrentBoardState(state: BoardProcessState | undefined, generation: number): boolean {
  return state !== undefined && !state.disposed && state.generation === generation && activeBoardProcess === state;
}

function stopActiveBoardProcess(): void {
  const current = activeBoardProcess;
  if (current === undefined) {
    return;
  }
  current.disposed = true;
  activeBoardProcess = undefined;
  if (!current.child.killed) {
    current.child.kill('SIGTERM');
  }
}

/** Runs `<binaryPath> board --serve --http-address 127.0.0.1:0` in `cwd`, streaming stderr and the canonical URL line from stdout into `outputChannel`. */
function runBoardProcess(
  binaryPath: string,
  cwd: string,
  outputChannel: vscode.OutputChannel,
  generation: number,
  deps: ShowBoardCommandDeps,
): BoardProcessState {
  const child = deps.spawn(binaryPath, ['board', '--serve', '--http-address', '127.0.0.1:0'], { cwd, shell: false });
  const state: BoardProcessState = {
    generation,
    child,
    url: null,
    disposed: false,
    ready: Promise.resolve(),
  };

  let bufferedLine = '';
  let settled = false;

  state.ready = new Promise<void>((resolve, reject) => {
    const resolveOnce = (): void => {
      if (settled) {
        return;
      }
      settled = true;
      resolve();
    };

    const rejectOnce = (message: string): void => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error(message));
    };

    const isCurrent = (): boolean => isCurrentBoardState(state, generation);

    child.stdout?.on('data', (chunk: Buffer | string) => {
      if (!isCurrent()) {
        return;
      }

      const text = chunk.toString();
      const parts = `${bufferedLine}${text}`.split(/\r?\n/);
      bufferedLine = parts.pop() ?? '';

      for (const line of parts) {
        const trimmedLine = line.trim();
        if (trimmedLine === '') {
          continue;
        }
        const url = deps.extractBoardServeURL(trimmedLine);
        if (url !== null) {
          state.url = url;
          resolveOnce();
          return;
        }
        outputChannel.appendLine(trimmedLine);
      }
    });

    child.stderr?.on('data', (chunk: Buffer | string) => {
      if (!isCurrent()) {
        return;
      }
      outputChannel.append(chunk.toString());
    });

    child.once('error', (err) => {
      if (!isCurrent()) {
        resolveOnce();
        return;
      }
      rejectOnce(err.message);
    });

    child.once('close', (code) => {
      if (!isCurrent()) {
        resolveOnce();
        return;
      }

      if (bufferedLine !== '') {
        const url = deps.extractBoardServeURL(bufferedLine);
        if (url !== null) {
          state.url = url;
          resolveOnce();
          return;
        }
        outputChannel.appendLine(bufferedLine.trim());
      }

      if (state.url === null) {
        rejectOnce(`board process exited before reporting a startup URL (code ${code ?? 'null'})`);
        return;
      }

      resolveOnce();
    });
  });

  return state;
}

async function openBoard(url: string, openExternal: typeof vscode.env.openExternal): Promise<void> {
  await openExternal(vscode.Uri.parse(url));
}

export function createBoardCommandController(deps: ShowBoardCommandDeps): {
  showBoard: () => Promise<void>;
  dispose: () => void;
} {
  const showBoard = async (): Promise<void> => {
    const generation = ++activeBoardGeneration;
    stopActiveBoardProcess();

    const target = await deps.resolveTargetWorkspaceFolder();

    if (generation !== activeBoardGeneration) {
      return;
    }

    if (target.kind === 'no-folders-open') {
      await deps.showErrorMessage('Open a folder first to view the Rhizome status board.');
      return;
    }
    if (target.kind === 'cancelled') {
      await deps.showErrorMessage('Select a workspace folder to view the Rhizome status board.');
      return;
    }

    const folder = target.folder;

    const resolution = deps.getLastResolution();
    if (!resolution || resolution.binaryPath === null) {
      await deps.showResolutionFailure();
      return;
    }

    const outputChannel = deps.getOutputChannel();
    outputChannel.appendLine(`[info] Running "rhizome-mcp board --serve" in ${folder.uri.fsPath}`);

    let state: BoardProcessState | undefined;

    try {
      state = runBoardProcess(resolution.binaryPath, folder.uri.fsPath, outputChannel, generation, deps);
      activeBoardProcess = state;

      await state.ready;
      if (!isCurrentBoardState(state, generation) || state.url === null) {
        return;
      }
      await openBoard(state.url, deps.openExternal);
    } catch (err) {
      if (generation !== activeBoardGeneration || (state !== undefined && !isCurrentBoardState(state, generation))) {
        return;
      }

      if (state !== undefined && isCurrentBoardState(state, generation)) {
        state.disposed = true;
        activeBoardProcess = undefined;
        if (!state.child.killed) {
          state.child.kill('SIGTERM');
        }
      }

      const message = err instanceof Error ? err.message : String(err);
      outputChannel.appendLine(`[error] failed to run "rhizome-mcp board --serve": ${message}`);
      await deps.showErrorMessage('Failed to run rhizome-mcp board. See the "Rhizome MCP" output channel for details.');
    }
  };

  const dispose = (): void => {
    activeBoardGeneration += 1;
    stopActiveBoardProcess();
  };

  return { showBoard, dispose };
}

/** Registers the `rhizome-mcp.showBoard` command. */
export function registerShowBoardCommand(): vscode.Disposable {
  const controller = createBoardCommandController({
    resolveTargetWorkspaceFolder,
    getLastResolution,
    getOutputChannel,
    showResolutionFailure,
    openExternal: vscode.env.openExternal,
    showErrorMessage: vscode.window.showErrorMessage,
    spawn,
    extractBoardServeURL,
  });

  return vscode.commands.registerCommand('rhizome-mcp.showBoard', async () => {
    await controller.showBoard();
  });
}

export function disposeBoardProcess(): void {
  activeBoardGeneration += 1;
  stopActiveBoardProcess();
}
