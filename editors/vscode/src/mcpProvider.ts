/**
 * Pure, dependency-free logic backing the `rhizome-mcp.servers` MCP server
 * definition provider:
 *
 *   - the label-computation rule (single- vs. multi-root workspaces), and
 *   - detection of an existing `rhizome-mcp` entry in a workspace folder's
 *     `.vscode/mcp.json` (the duplicate-registration guard — if a folder
 *     already has one, this extension must not contribute a second
 *     definition for the same server).
 *
 * Nothing in this file imports `vscode`, enumerates real workspace folders,
 * or touches the real filesystem, so it can be exercised with plain
 * `node --test` (see `../test/mcpProvider.test.js`) without booting VS Code.
 * The thin glue that supplies real `vscode.workspace` state, real file
 * reads, and registers the actual provider lives in `./mcpServerProvider.ts`.
 */

/** Name of the marker file `rhizome-mcp init` writes at a workspace/repo root. */
export const TRACKER_FILENAME = '.agent-tracker.json';

/**
 * Computes the human-readable label for the MCP server definition
 * contributed for a given workspace folder.
 *
 * `"rhizome"` when there's exactly one workspace folder in the whole
 * workspace, `` `rhizome (${folderName})` `` once there's more than one —
 * the folder count that matters here is the *total* number of workspace
 * folders, not just the number that end up producing a server definition
 * (e.g. some may be skipped for lacking `.agent-tracker.json`, or via the
 * duplicate-registration guard).
 */
export function computeServerLabel(folderName: string, totalWorkspaceFolders: number): string {
  return totalWorkspaceFolders > 1 ? `rhizome (${folderName})` : 'rhizome';
}

/**
 * Parses the raw contents of a workspace folder's `.vscode/mcp.json` and
 * reports whether it already registers a server literally named
 * `rhizome-mcp` under a top-level `servers` object — the shape written by
 * `rhizome-mcp connect vscode`:
 *
 * ```json
 * { "servers": { "rhizome-mcp": { "type": "stdio", "command": "...", "args": ["serve"] } } }
 * ```
 *
 * Anything that isn't well-formed JSON shaped like that (parse error,
 * non-object root, missing/non-object `servers`) is treated as "does not
 * already have rhizome-mcp" — i.e. this returns `false` rather than
 * throwing. Per spec, a malformed `.vscode/mcp.json` must never be treated
 * as already covering rhizome (which would wrongly suppress this
 * extension's own registration) and must never crash the caller.
 */
export function hasRhizomeServerEntry(mcpJsonRaw: string): boolean {
  let parsed: unknown;
  try {
    parsed = JSON.parse(mcpJsonRaw);
  } catch {
    return false;
  }

  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return false;
  }

  const servers = (parsed as Record<string, unknown>).servers;
  if (typeof servers !== 'object' || servers === null || Array.isArray(servers)) {
    return false;
  }

  return Object.prototype.hasOwnProperty.call(servers, 'rhizome-mcp');
}
