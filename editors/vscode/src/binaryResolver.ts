/**
 * Pure, dependency-injected logic for locating and validating the
 * `rhizome-mcp` server binary used by this extension.
 *
 * Resolution order:
 *   1. The `rhizome.serverPath` setting, if configured. An invalid value
 *      (missing file, not executable, etc.) is surfaced as a failure rather
 *      than silently falling through to the next step.
 *   2. The binary bundled with the extension at `<extensionPath>/bin/`
 *      (staged there by the packaging pipeline before a VSIX is produced;
 *      absent in a plain source checkout).
 *   3. `rhizome-mcp` found on PATH.
 *
 * Nothing in this file imports `vscode`, spawns a real process, or touches
 * the real filesystem — every OS-facing effect (fs access, chmod, spawning
 * `--version`) is injected as a plain function/object so the decision logic
 * here can be exercised with plain `node:test` (see
 * `../test/binaryResolver.test.js`) without booting VS Code. The thin glue
 * that supplies real implementations of these dependencies and talks to the
 * `vscode` API lives in `./activation.ts`.
 */

import * as nodePath from 'node:path';

export type BinarySource = 'override' | 'bundled' | 'path';

export interface ResolveSuccess {
  ok: true;
  source: BinarySource;
  binaryPath: string;
}

export interface ResolveFailure {
  ok: false;
  reason: 'invalid-override' | 'not-found';
  message: string;
}

export type ResolveResult = ResolveSuccess | ResolveFailure;

/** The subset of `fs.Stats` this module relies on. */
export interface StatLike {
  mode: number;
  isFile(): boolean;
}

/** The subset of the `fs` module this module relies on. */
export interface FsLike {
  existsSync(path: string): boolean;
  statSync(path: string): StatLike;
  chmodSync(path: string, mode: number): void;
}

export interface ResolveDeps {
  platform: NodeJS.Platform;
  env: NodeJS.ProcessEnv;
  /** The extension's install directory (`context.extensionUri.fsPath`). */
  extensionPath: string;
  fs: FsLike;
}

export interface SpawnResult {
  stdout: string;
  code: number | null;
}

/** Runs `<binaryPath> <args>` without a shell and resolves with captured stdout. */
export type SpawnFn = (binaryPath: string, args: string[]) => Promise<SpawnResult>;

export interface VersionInfo {
  /** Raw stdout captured from `<binary> --version`. */
  raw: string;
  /** Parsed version token, or null if the output could not be parsed. */
  version: string | null;
}

export interface Logger {
  info(message: string): void;
  warn(message: string): void;
}

export interface ResolveAndValidateDeps extends ResolveDeps {
  /** This extension's own version, from its package.json. */
  extensionVersion: string;
  spawnFn: SpawnFn;
  logger: Logger;
  /** Memoizes the version handshake per binary path for the extension's lifetime. */
  versionCache: Map<string, VersionInfo>;
}

export interface ResolveAndValidateResult {
  binaryPath: string | null;
  source: BinarySource | null;
  version: string | null;
  failure: ResolveFailure | null;
}

function pathModuleFor(platform: NodeJS.Platform): typeof nodePath.win32 {
  return platform === 'win32' ? nodePath.win32 : nodePath.posix;
}

/** `rhizome-mcp.exe` on Windows, `rhizome-mcp` everywhere else. */
export function getBundledBinaryName(platform: NodeJS.Platform): string {
  return platform === 'win32' ? 'rhizome-mcp.exe' : 'rhizome-mcp';
}

/** Absolute path to the binary bundled at packaging time, e.g. `<ext>/bin/rhizome-mcp`. */
export function getBundledBinaryPath(extensionPath: string, platform: NodeJS.Platform): string {
  return pathModuleFor(platform).join(extensionPath, 'bin', getBundledBinaryName(platform));
}

/** Whether a POSIX file mode has any of the executable bits set. */
export function isExecutableMode(mode: number): boolean {
  return (mode & 0o111) !== 0;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function resolveOverride(overridePath: string, deps: ResolveDeps): ResolveResult {
  if (!deps.fs.existsSync(overridePath)) {
    return {
      ok: false,
      reason: 'invalid-override',
      message: `The configured rhizome.serverPath ("${overridePath}") does not exist.`,
    };
  }

  let stat: StatLike;
  try {
    stat = deps.fs.statSync(overridePath);
  } catch (err) {
    return {
      ok: false,
      reason: 'invalid-override',
      message: `The configured rhizome.serverPath ("${overridePath}") could not be read: ${errorMessage(err)}`,
    };
  }

  if (!stat.isFile()) {
    return {
      ok: false,
      reason: 'invalid-override',
      message: `The configured rhizome.serverPath ("${overridePath}") is not a file.`,
    };
  }

  // Windows has no executable-bit concept worth checking here; existence is enough.
  if (deps.platform !== 'win32' && !isExecutableMode(stat.mode)) {
    return {
      ok: false,
      reason: 'invalid-override',
      message: `The configured rhizome.serverPath ("${overridePath}") exists but is not executable.`,
    };
  }

  return { ok: true, source: 'override', binaryPath: overridePath };
}

function resolveBundled(deps: ResolveDeps): ResolveSuccess | null {
  const bundledPath = getBundledBinaryPath(deps.extensionPath, deps.platform);
  if (!deps.fs.existsSync(bundledPath)) {
    return null;
  }

  let stat: StatLike;
  try {
    stat = deps.fs.statSync(bundledPath);
  } catch {
    // Unreadable despite existsSync succeeding (e.g. a race, or a broken
    // symlink) — treat as "not bundled" and fall through to PATH.
    return null;
  }
  if (!stat.isFile()) {
    return null;
  }

  if (deps.platform !== 'win32') {
    // VSIX zip extraction does not reliably preserve the executable bit, so
    // this runs every time the bundled binary is resolved. Idempotent and cheap.
    try {
      deps.fs.chmodSync(bundledPath, 0o755);
    } catch {
      /* best-effort: if chmod genuinely fails, spawning the binary below will surface a clear error */
    }
  }

  return { ok: true, source: 'bundled', binaryPath: bundledPath };
}

function resolveFromPath(deps: ResolveDeps): ResolveSuccess | null {
  const pm = pathModuleFor(deps.platform);
  const pathEnvValue = deps.env.PATH ?? deps.env.Path ?? deps.env.path ?? '';
  if (!pathEnvValue) {
    return null;
  }

  const candidateNames = deps.platform === 'win32' ? ['rhizome-mcp.exe', 'rhizome-mcp'] : ['rhizome-mcp'];
  const dirs = pathEnvValue.split(pm.delimiter).filter((dir) => dir.length > 0);

  for (const dir of dirs) {
    for (const name of candidateNames) {
      const candidate = pm.join(dir, name);
      if (!deps.fs.existsSync(candidate)) {
        continue;
      }
      try {
        if (deps.fs.statSync(candidate).isFile()) {
          return { ok: true, source: 'path', binaryPath: candidate };
        }
      } catch {
        // Unreadable PATH entry; keep searching the rest of PATH.
      }
    }
  }

  return null;
}

/**
 * Resolves which `rhizome-mcp` binary to use, following the precedence
 * described at the top of this file. An invalid `rhizome.serverPath`
 * override is a hard failure — it never falls through to the bundled
 * binary or PATH.
 */
export function resolveBinary(overridePath: string | undefined, deps: ResolveDeps): ResolveResult {
  const trimmedOverride = overridePath?.trim();
  if (trimmedOverride) {
    return resolveOverride(trimmedOverride, deps);
  }

  const bundled = resolveBundled(deps);
  if (bundled) {
    return bundled;
  }

  const onPath = resolveFromPath(deps);
  if (onPath) {
    return onPath;
  }

  return {
    ok: false,
    reason: 'not-found',
    message:
      'Could not locate the rhizome-mcp server binary: no rhizome.serverPath override is configured, ' +
      'no bundled binary was found, and rhizome-mcp is not on PATH.',
  };
}

// Matches main.go's `fmt.Sprintf("rhizome-mcp %s (commit %s, built %s)", version, commit, date)`,
// e.g. "rhizome-mcp v1.0.0-beta.2 (commit 1d3865e, built 2026-07-23T16:03:00Z)".
const VERSION_OUTPUT_PATTERN = /^rhizome-mcp\s+(\S+)\s+\(commit\s+\S+,\s+built\s+\S+\)\s*$/;

/** Extracts the version token from `<binary> --version` output, or null if it doesn't match. */
export function parseVersionOutput(output: string): string | null {
  const match = VERSION_OUTPUT_PATTERN.exec(output.trim());
  return match ? match[1] : null;
}

/**
 * Runs `<binaryPath> --version` and parses the result, memoizing per
 * `binaryPath` in `cache` so the process is only spawned once for the
 * lifetime of the extension.
 */
export async function getBinaryVersion(
  binaryPath: string,
  spawnFn: SpawnFn,
  cache: Map<string, VersionInfo>,
): Promise<VersionInfo> {
  const cached = cache.get(binaryPath);
  if (cached) {
    return cached;
  }

  let stdout: string;
  try {
    const result = await spawnFn(binaryPath, ['--version']);
    stdout = result.stdout;
  } catch {
    const info: VersionInfo = { raw: '', version: null };
    cache.set(binaryPath, info);
    return info;
  }

  const info: VersionInfo = { raw: stdout, version: parseVersionOutput(stdout) };
  cache.set(binaryPath, info);
  return info;
}

/**
 * Returns a warning message if `resolvedVersion` differs from
 * `extensionVersion` for a binary that did NOT come from the bundled path
 * (bundled binaries are versioned in lockstep with the extension by the
 * packaging pipeline, so they're never compared). Returns null when there's
 * nothing to warn about.
 */
export function checkVersionMismatch(
  source: BinarySource,
  resolvedVersion: string,
  extensionVersion: string,
): string | null {
  if (source === 'bundled') {
    return null;
  }
  if (resolvedVersion === extensionVersion) {
    return null;
  }
  return (
    `rhizome-mcp binary reports version "${resolvedVersion}", which differs from this extension's ` +
    `version "${extensionVersion}". This is expected during the pre-1.0 beta period.`
  );
}

/**
 * Resolves the binary, runs the version handshake, and logs any warnings
 * (parse failure, version mismatch). Never throws for expected failure
 * modes: an unresolved binary or unparseable version output both come back
 * as data (`failure` set, or `version: null`) rather than a thrown error.
 */
export async function resolveAndValidateBinary(
  overridePath: string | undefined,
  deps: ResolveAndValidateDeps,
): Promise<ResolveAndValidateResult> {
  const resolved = resolveBinary(overridePath, deps);
  if (!resolved.ok) {
    deps.logger.warn(resolved.message);
    return { binaryPath: null, source: null, version: null, failure: resolved };
  }

  deps.logger.info(`Resolved rhizome-mcp binary from ${resolved.source}: ${resolved.binaryPath}`);

  const versionInfo = await getBinaryVersion(resolved.binaryPath, deps.spawnFn, deps.versionCache);
  if (versionInfo.version === null) {
    deps.logger.warn(
      `Could not determine the rhizome-mcp binary's version from its --version output ` +
        `(${JSON.stringify(versionInfo.raw)}); continuing with an unknown version.`,
    );
  } else {
    const mismatch = checkVersionMismatch(resolved.source, versionInfo.version, deps.extensionVersion);
    if (mismatch) {
      deps.logger.warn(mismatch);
    }
  }

  return {
    binaryPath: resolved.binaryPath,
    source: resolved.source,
    version: versionInfo.version,
    failure: null,
  };
}
