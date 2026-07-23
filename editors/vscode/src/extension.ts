import * as vscode from 'vscode';
import { activateRhizome, getOutputChannel } from './activation';
import { registerRhizomeMcpServerProvider } from './mcpServerProvider';

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  try {
    // Resolves and validates the rhizome-mcp binary, logging the outcome and
    // showing the standard failure notification if nothing resolves.
    const resolution = await activateRhizome(context);

    // Only register the MCP server definition provider when resolution
    // actually produced a binary to serve. `binaryPath` is non-null exactly
    // when `failure` is null (see ResolveAndValidateResult); `version` may
    // still legitimately be null on success (the binary's `--version` output
    // didn't parse) — that's not a reason to skip registering a working
    // server, so it's only gated on `binaryPath`, not on `version` too.
    if (resolution.binaryPath !== null) {
      registerRhizomeMcpServerProvider(
        context,
        { binaryPath: resolution.binaryPath, version: resolution.version },
        getOutputChannel(),
      );
    }
    // If resolution failed, activateRhizome already showed the standard
    // failure notification — there's no binary to serve, so nothing more to do.
  } catch (err) {
    // activateRhizome already handles its own expected failure modes; this
    // is a last-resort guard so activation itself can never throw.
    console.error('rhizome-mcp: activation failed unexpectedly', err);
  }
}

export function deactivate(): void {}
