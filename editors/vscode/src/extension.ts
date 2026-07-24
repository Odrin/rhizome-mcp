import * as vscode from 'vscode';
import { activateRhizome, getOutputChannel } from './activation';
import { registerInitCommand } from './initCommand';
import { registerShowBoardCommand } from './boardCommand';
import { registerRhizomeMcpServerProvider, type RhizomeMcpServerProvider } from './mcpServerProvider';

// Module-level reference to the registered MCP server definition provider
// (if any), so the `rhizome-mcp.init` command can call its `refresh()`
// after a successful init without extension.ts needing to pass a live
// provider reference around before one necessarily exists. Commands are
// registered unconditionally at activation, before resolution is known to
// have succeeded, so they read this indirectly via a getter instead.
let mcpServerProvider: RhizomeMcpServerProvider | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // Registered unconditionally (independent of whether binary resolution
  // below succeeds) so the commands always exist in the Command Palette;
  // each command re-checks `getLastResolution()` itself at invocation time
  // and shows the standard failure UX if there's still no binary.
  context.subscriptions.push(
    registerInitCommand(() => mcpServerProvider),
    registerShowBoardCommand(),
  );

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
      mcpServerProvider = registerRhizomeMcpServerProvider(
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

export function deactivate(): void {
  mcpServerProvider = undefined;
}
