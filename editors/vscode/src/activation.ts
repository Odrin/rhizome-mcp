/**
 * Thin `vscode`-facing glue around `./binaryResolver.ts`'s pure resolution
 * logic: reads the `rhizome.serverPath` setting, supplies real fs/spawn
 * implementations, and turns a resolution failure into the one-notification
 * failure UX. Kept deliberately small — anything that doesn't need the
 * `vscode` API belongs in binaryResolver.ts instead, where it can be unit
 * tested without VS Code.
 */

import { spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as vscode from 'vscode';
import {
  resolveAndValidateBinary,
  type ResolveAndValidateResult,
  type SpawnResult,
  type VersionInfo,
} from './binaryResolver';

const INSTALL_INSTRUCTIONS_URL = 'https://github.com/Odrin/rhizome-mcp#readme';

let outputChannel: vscode.OutputChannel | undefined;

/** The extension's shared output channel, created lazily. Exported so other modules (e.g. the MCP server provider) can log to the same channel. */
export function getOutputChannel(): vscode.OutputChannel {
  outputChannel ??= vscode.window.createOutputChannel('Rhizome MCP');
  return outputChannel;
}

// Memoizes the `--version` handshake per binary path for the lifetime of the extension host.
const versionCache = new Map<string, VersionInfo>();

let lastResolution: ResolveAndValidateResult | undefined;

/** The outcome of the most recent activation-time resolution, if any. Exposed for later issues/tests. */
export function getLastResolution(): ResolveAndValidateResult | undefined {
  return lastResolution;
}

function spawnVersionCheck(binaryPath: string, args: string[]): Promise<SpawnResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath, args, { shell: false });

    let stdout = '';
    child.stdout?.on('data', (chunk: Buffer | string) => {
      stdout += chunk.toString();
    });
    child.once('error', (err) => reject(err));
    child.once('close', (code) => resolve({ stdout, code }));
  });
}

async function showResolutionFailure(): Promise<void> {
  const selection = await vscode.window.showErrorMessage(
    'Rhizome MCP could not locate the rhizome-mcp server binary. Configure rhizome.serverPath, ' +
      'reinstall the extension so its bundled binary is present, or install rhizome-mcp on your PATH.',
    'Open Settings',
    'Install Instructions',
  );

  if (selection === 'Open Settings') {
    await vscode.commands.executeCommand('workbench.action.openSettings', 'rhizome.serverPath');
  } else if (selection === 'Install Instructions') {
    await vscode.env.openExternal(vscode.Uri.parse(INSTALL_INSTRUCTIONS_URL));
  }
}

/**
 * Resolves and validates the rhizome-mcp binary for activation. Registering
 * the MCP server definition with the resolved path is out of scope here
 * (a later issue) — this only resolves, logs, and (on failure) shows the
 * standard notification. Never throws: any unexpected error is caught,
 * logged, and reported through the same failure UX.
 */
export async function activateRhizome(context: vscode.ExtensionContext): Promise<ResolveAndValidateResult> {
  const channel = getOutputChannel();

  try {
    const config = vscode.workspace.getConfiguration('rhizome');
    const overridePath = config.get<string>('serverPath');

    const result = await resolveAndValidateBinary(overridePath, {
      platform: process.platform,
      env: process.env,
      extensionPath: context.extensionUri.fsPath,
      extensionVersion: String(context.extension.packageJSON.version ?? ''),
      fs,
      spawnFn: spawnVersionCheck,
      logger: {
        info: (message) => channel.appendLine(`[info] ${message}`),
        warn: (message) => {
          channel.appendLine(`[warn] ${message}`);
          console.warn(`rhizome-mcp: ${message}`);
        },
      },
      versionCache,
    });

    lastResolution = result;

    if (result.failure) {
      await showResolutionFailure();
    }

    return result;
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    channel.appendLine(`[error] unexpected error while resolving the rhizome-mcp binary: ${message}`);
    console.error('rhizome-mcp: unexpected error while resolving the binary', err);

    const failureResult: ResolveAndValidateResult = {
      binaryPath: null,
      source: null,
      version: null,
      failure: { ok: false, reason: 'not-found', message },
    };
    lastResolution = failureResult;

    await showResolutionFailure().catch((notifyErr: unknown) => {
      console.error('rhizome-mcp: failed to show the resolution-failure notification', notifyErr);
    });

    return failureResult;
  }
}
