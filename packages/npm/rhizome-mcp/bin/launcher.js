#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');
const path = require('node:path');
const { resolveBinaryPath } = require(path.join(__dirname, '..', 'lib', 'platform.js'));

function printUnresolvedError(reason, packageName) {
  const expected = packageName || `@rhizome-mcp/${process.platform}-${process.arch}`;
  const lines = [
    `rhizome-mcp: could not find the "${expected}" platform binary package.`,
    '',
    reason === 'unsupported-platform'
      ? `Your platform/architecture (${process.platform}/${process.arch}) is not published by rhizome-mcp on npm.`
      : `"${expected}" was not installed. This happens if it was installed with --no-optional, ` +
        '--ignore-scripts, or a similar flag that skips optional dependencies, or if npm chose not ' +
        'to install it for this platform.',
    '',
    'As a fallback, install rhizome-mcp directly using the installer script from the main repository README:',
    '  curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh',
    '  (see https://github.com/Odrin/rhizome-mcp#quick-start for the Windows/PowerShell equivalent)',
  ];
  process.stderr.write(lines.join('\n') + '\n');
}

function main() {
  const result = resolveBinaryPath(process.platform, process.arch, require.resolve);

  if (!result.ok) {
    printUnresolvedError(result.reason, result.packageName);
    process.exitCode = 1;
    return;
  }

  const child = spawn(result.binaryPath, process.argv.slice(2), { stdio: 'inherit' });

  const forwardSignal = (signal) => {
    child.kill(signal);
  };
  process.on('SIGTERM', forwardSignal);
  process.on('SIGINT', forwardSignal);

  child.on('error', (err) => {
    process.stderr.write(`rhizome-mcp: failed to launch "${result.binaryPath}": ${err.message}\n`);
    process.exitCode = 1;
  });

  child.on('exit', (code, signal) => {
    if (signal) {
      // Child died from a signal (e.g. it forwarded a signal to itself, or
      // was killed directly) rather than exiting normally: reflect that by
      // exiting with the conventional 128+signal code.
      const signalNumber = require('node:os').constants.signals[signal];
      process.exitCode = typeof signalNumber === 'number' ? 128 + signalNumber : 1;
      return;
    }
    process.exitCode = code === null ? 1 : code;
  });
}

main();
