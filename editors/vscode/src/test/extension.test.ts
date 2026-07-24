import * as assert from 'assert';
import * as vscode from 'vscode';

suite('Extension activation', () => {
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
});
