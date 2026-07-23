import * as assert from 'assert';
import * as vscode from 'vscode';

suite('Extension activation', () => {
  test('extension is present and activates', async () => {
    const ext = vscode.extensions.getExtension('odrin.rhizome-mcp');
    assert.ok(ext, 'extension should be discoverable');
    await ext?.activate();
    assert.ok(ext?.isActive);
  });
});
