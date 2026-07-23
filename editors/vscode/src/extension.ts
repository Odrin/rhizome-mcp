import * as vscode from 'vscode';
import { activateRhizome } from './activation';

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  try {
    // Resolves and validates the rhizome-mcp binary, logging the outcome and
    // showing the standard failure notification if nothing resolves.
    // Registering the MCP server definition with the resolved path is a
    // separate, later issue.
    await activateRhizome(context);
  } catch (err) {
    // activateRhizome already handles its own expected failure modes; this
    // is a last-resort guard so activation itself can never throw.
    console.error('rhizome-mcp: activation failed unexpectedly', err);
  }
}

export function deactivate(): void {}
