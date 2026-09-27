import * as assert from 'assert';
import { EventEmitter } from 'node:events';
import * as vscode from 'vscode';
import { createBoardCommandController, resetBoardCommandStateForTests } from '../boardCommand';

function makeOutputChannel() {
  const lines: string[] = [];
  return {
    append: (chunk: string) => {
      lines.push(chunk);
    },
    appendLine: (line: string) => {
      lines.push(line);
    },
    clear: () => undefined,
    show: () => undefined,
    dispose: () => undefined,
    name: 'Rhizome MCP',
    lines,
  };
}

function makeChildProcess() {
  const child = new EventEmitter() as EventEmitter & {
    stdout: EventEmitter;
    stderr: EventEmitter;
    killed: boolean;
    kill: (signal?: NodeJS.Signals | number) => boolean;
  };

  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.killed = false;
  child.kill = (signal?: NodeJS.Signals | number) => {
    child.killed = true;
    child.emit('close', 0, signal);
    return true;
  };

  return child;
}

async function flushAsync() {
  await new Promise<void>((resolve) => setImmediate(resolve));
}

function makeWorkspaceFolder(): vscode.WorkspaceFolder {
  return {
    name: 'workspace',
    index: 0,
    uri: vscode.Uri.file('/workspace'),
    isUntitled: false,
  } as vscode.WorkspaceFolder;
}

function makeResolution(): {
  binaryPath: string;
  version: string;
  source: null;
  failure: null;
} {
  return { binaryPath: '/bin/rhizome-mcp', version: '1.0.0', source: null, failure: null };
}

suite('Extension activation', () => {
  teardown(() => {
    resetBoardCommandStateForTests();
  });

  test('extension is present and activates', async () => {
    const ext = vscode.extensions.getExtension('odrin.rhizome-mcp');
    assert.ok(ext, 'extension should be discoverable');
    await ext?.activate();
    assert.ok(ext?.isActive);
  });

  test('registers the rhizome-mcp.init and rhizome-mcp.showBoard commands', async () => {
    const ext = vscode.extensions.getExtension('odrin.rhizome-mcp');
    await ext?.activate();

    const commands = await vscode.commands.getCommands(true);
    assert.ok(commands.includes('rhizome-mcp.init'), 'expected rhizome-mcp.init to be registered');
    assert.ok(commands.includes('rhizome-mcp.showBoard'), 'expected rhizome-mcp.showBoard to be registered');
  });

  test('board overlapping opens terminate the earlier child and only the newest URL is opened', async () => {
    const output = makeOutputChannel();
    const openCalls: string[] = [];
    const errorCalls: string[] = [];
    const firstChild = makeChildProcess();
    const secondChild = makeChildProcess();
    const spawnQueue = [firstChild, secondChild];

    const controller = createBoardCommandController({
      resolveTargetWorkspaceFolder: async () => ({
        kind: 'folder',
        folder: makeWorkspaceFolder(),
      }),
      getLastResolution: () => makeResolution(),
      getOutputChannel: () => output as never,
      showResolutionFailure: async () => undefined,
      openExternal: async (uri: vscode.Uri) => {
        openCalls.push(uri.toString());
        return true;
      },
      showErrorMessage: async (message: string) => {
        errorCalls.push(message);
        return undefined;
      },
      spawn: (() => {
        const child = spawnQueue.shift() ?? secondChild;
        return child as never;
      }) as unknown as typeof import('node:child_process').spawn,
      extractBoardServeURL: (value: string) => {
        const match = value.match(/https?:\/\/[^\s]+/);
        return match ? match[0] : null;
      },
    });

    const firstStart = controller.showBoard();
    await flushAsync();
    const secondStart = controller.showBoard();
    await flushAsync();

    assert.strictEqual(firstChild.killed, true, 'older child should be terminated when a newer open starts');
    assert.strictEqual(secondChild.killed, false, 'newer child should remain active');

    secondChild.stdout.emit('data', 'http://127.0.0.1:4321\n');
    secondChild.emit('close', 0);

    await Promise.all([firstStart, secondStart]);
    assert.deepStrictEqual(openCalls, ['http://127.0.0.1:4321/']);

    firstChild.stdout.emit('data', 'http://127.0.0.1:9999\n');
    firstChild.emit('error', new Error('late first failure'));
    assert.deepStrictEqual(openCalls, ['http://127.0.0.1:4321/']);
    assert.deepStrictEqual(errorCalls, []);
    controller.dispose();
  });

  test('board dispose during startup terminates the child and ignores late URL/error events', async () => {
    const openCalls: string[] = [];
    const errorCalls: string[] = [];
    const output = makeOutputChannel();
    const child = makeChildProcess();

    const controller = createBoardCommandController({
      resolveTargetWorkspaceFolder: async () => ({
        kind: 'folder',
        folder: makeWorkspaceFolder(),
      }),
      getLastResolution: () => makeResolution(),
      getOutputChannel: () => output as never,
      showResolutionFailure: async () => undefined,
      openExternal: async (uri: vscode.Uri) => {
        openCalls.push(uri.toString());
        return true;
      },
      showErrorMessage: async (message: string) => {
        errorCalls.push(message);
        return undefined;
      },
      spawn: (() => child) as unknown as typeof import('node:child_process').spawn,
      extractBoardServeURL: (value: string) => {
        const match = value.match(/https?:\/\/[^\s]+/);
        return match ? match[0] : null;
      },
    });

    const running = controller.showBoard();
    await flushAsync();
    controller.dispose();

    assert.strictEqual(child.killed, true, 'startup child should be terminated during disposal');

    child.stdout.emit('data', 'http://127.0.0.1:5678\n');
    child.emit('error', new Error('late disposal error'));

    await running;
    assert.deepStrictEqual(openCalls, []);
    assert.deepStrictEqual(errorCalls, []);
  });

  test('board normal startup opens the URL and remains owned until disposal', async () => {
    const openCalls: string[] = [];
    const errorCalls: string[] = [];
    const output = makeOutputChannel();
    const child = makeChildProcess();

    const controller = createBoardCommandController({
      resolveTargetWorkspaceFolder: async () => ({
        kind: 'folder',
        folder: makeWorkspaceFolder(),
      }),
      getLastResolution: () => makeResolution(),
      getOutputChannel: () => output as never,
      showResolutionFailure: async () => undefined,
      openExternal: async (uri: vscode.Uri) => {
        openCalls.push(uri.toString());
        return true;
      },
      showErrorMessage: async (message: string) => {
        errorCalls.push(message);
        return undefined;
      },
      spawn: (() => child) as unknown as typeof import('node:child_process').spawn,
      extractBoardServeURL: (value: string) => {
        const match = value.match(/https?:\/\/[^\s]+/);
        return match ? match[0] : null;
      },
    });

    const showPromise = controller.showBoard();
    await flushAsync();
    child.stdout.emit('data', 'http://127.0.0.1:4242\n');
    child.emit('close', 0);
    await showPromise;

    assert.deepStrictEqual(openCalls, ['http://127.0.0.1:4242/']);
    controller.dispose();
    assert.deepStrictEqual(errorCalls, []);
  });

  test('board synchronous spawn failure still shows the standard user-visible error text', async () => {
    const output = makeOutputChannel();
    const errorCalls: string[] = [];

    const controller = createBoardCommandController({
      resolveTargetWorkspaceFolder: async () => ({
        kind: 'folder',
        folder: makeWorkspaceFolder(),
      }),
      getLastResolution: () => makeResolution(),
      getOutputChannel: () => output as never,
      showResolutionFailure: async () => undefined,
      openExternal: async () => true,
      showErrorMessage: async (message: string) => {
        errorCalls.push(message);
        return undefined;
      },
      spawn: (() => {
        throw new Error('startup boom');
      }) as unknown as typeof import('node:child_process').spawn,
      extractBoardServeURL: (value: string) => {
        const match = value.match(/https?:\/\/[^\s]+/);
        return match ? match[0] : null;
      },
    });

    await controller.showBoard();

    assert.ok(errorCalls.includes('Failed to run rhizome-mcp board. See the "Rhizome MCP" output channel for details.'));
    assert.ok(output.lines.some((line) => line.includes('startup boom')));
  });

  test('board genuine startup failure still shows the standard user-visible error text', async () => {
    const output = makeOutputChannel();
    const errorCalls: string[] = [];
    const child = makeChildProcess();

    const controller = createBoardCommandController({
      resolveTargetWorkspaceFolder: async () => ({
        kind: 'folder',
        folder: makeWorkspaceFolder(),
      }),
      getLastResolution: () => makeResolution(),
      getOutputChannel: () => output as never,
      showResolutionFailure: async () => undefined,
      openExternal: async () => true,
      showErrorMessage: async (message: string) => {
        errorCalls.push(message);
        return undefined;
      },
      spawn: (() => child) as unknown as typeof import('node:child_process').spawn,
      extractBoardServeURL: (value: string) => {
        const match = value.match(/https?:\/\/[^\s]+/);
        return match ? match[0] : null;
      },
    });

    const showPromise = controller.showBoard();
    await flushAsync();
    child.emit('error', new Error('startup boom'));
    await showPromise.catch(() => undefined);

    assert.ok(errorCalls.includes('Failed to run rhizome-mcp board. See the "Rhizome MCP" output channel for details.'));
    assert.ok(output.lines.some((line) => line.includes('startup boom')));
  });
});
