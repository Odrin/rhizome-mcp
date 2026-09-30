import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import { deriveVersion } from './package-platforms.mjs';

test('beta and stable tags use distinct ordered versions above published history', () => {
  assert.deepEqual(deriveVersion({ tagOverride: 'v1.0.1-beta.2', fallbackVersion: '0.0.1' }), {
    version: '3.0.1003',
    preRelease: true,
    tag: 'v1.0.1-beta.2',
  });
  assert.deepEqual(deriveVersion({ tagOverride: 'v1.0.1', fallbackVersion: '0.0.1' }), {
    version: '3.0.2000',
    preRelease: false,
    tag: 'v1.0.1',
  });
});

const publishedVersions = [
  '1.5.2',
  '1.5.1',
  '1.5.0',
  '1.4.0',
  '1.3.3',
  '1.3.2',
  '1.3.1',
  '1.2.1',
  '1.2.0',
  '1.1.1005',
  '1.1.1004',
  '1.1.1003',
  '1.1.0',
  '1.0.1',
];

function mapped(tag) {
  return deriveVersion({ tagOverride: tag, fallbackVersion: '0.0.1' });
}

function compareVersions(left, right) {
  const leftComponents = left.split('.').map(Number);
  const rightComponents = right.split('.').map(Number);
  for (let index = 0; index < 3; index++) {
    if (leftComponents[index] !== rightComponents[index]) {
      return leftComponents[index] - rightComponents[index];
    }
  }
  return 0;
}

function assertOrdered(tags) {
  let previous = '1.5.2';
  const versions = new Set(publishedVersions);
  for (const tag of tags) {
    const result = mapped(tag);
    assert.match(result.version, /^\d+\.\d+\.\d+$/);
    assert.equal(result.tag, tag);
    assert.equal(result.preRelease, tag.includes('-beta.'));
    assert.ok(
      compareVersions(result.version, previous) > 0,
      `${tag}: ${result.version} must follow ${previous}`,
    );
    assert.ok(!versions.has(result.version), `${tag}: duplicate version ${result.version}`);
    versions.add(result.version);
    previous = result.version;
  }
}

test('historical and future release transitions remain strictly ordered', () => {
  assertOrdered([
    'v1.0.0-beta.0',
    'v1.0.0-beta.1',
    'v1.0.0-beta.998',
    'v1.0.0',
    'v1.0.1-beta.2',
    'v1.0.1-beta.3',
    'v1.0.1-beta.4',
    'v1.0.1-beta.5',
    'v1.0.1',
    'v1.0.2-beta.0',
    'v1.0.2',
    'v1.1.0-beta.0',
    'v1.1.0',
    'v1.2.0',
    'v1.2.1',
    'v1.3.1',
    'v1.3.2',
    'v1.3.3',
    'v1.4.0',
    'v1.5.0',
    'v1.5.1',
    'v1.5.2',
    'v1.5.3-beta.0',
    'v1.5.3-beta.1',
    'v1.5.3',
    'v1.5.4-beta.0',
    'v1.5.4',
    'v1.6.0-beta.0',
    'v1.6.0',
    'v2.0.0-beta.0',
    'v2.0.0',
  ]);
});

test('every beta slot precedes stable and the following patch', () => {
  const tags = [];
  for (let patch = 0; patch < 2; patch++) {
    for (let beta = 0; beta <= 998; beta++) {
      tags.push(`v1.0.${patch}-beta.${beta}`);
    }
    tags.push(`v1.0.${patch}`);
  }
  assertOrdered(tags);
  assert.equal(mapped('v1.0.2-beta.0').version, '3.0.2001');
});

test('representative product tuples are unique across epoch, minor and patch boundaries', () => {
  const tags = [];
  for (let major = 0; major <= 3; major++) {
    for (let minor = 0; minor <= 2; minor++) {
      for (let patch = 0; patch <= 3; patch++) {
        for (const beta of [0, 1, 2, 998]) {
          tags.push(`v${major}.${minor}.${patch}-beta.${beta}`);
        }
        tags.push(`v${major}.${minor}.${patch}`);
      }
    }
  }
  assertOrdered(tags);
});

test('numeric bounds permit a stable successor for the highest accepted beta tuple', () => {
  assert.equal(
    mapped('v2147483645.2147483647.2147482-beta.998').version,
    '2147483647.2147483647.2147482999',
  );
  assert.equal(
    mapped('v2147483645.2147483647.2147482').version,
    '2147483647.2147483647.2147483000',
  );
  assert.equal(mapped('v0.0.0-beta.0').version, '2.0.1');
  assert.equal(mapped('v0.0.0').version, '2.0.1000');
});

test('unsupported or noncanonical tags fail instead of colliding or overflowing', () => {
  for (const tag of [
    '1.0.0',
    'v01.0.0',
    'v1.00.0',
    'v1.0.00',
    'v1.0.0-beta.01',
    'v1.0.0-alpha.1',
    'v1.0.0-beta.-1',
    'v1.0.0-beta.999',
    'v1.0.0-beta.1000',
    'v1.0.0+build.1',
    'v2147483646.0.0',
    'v0.2147483648.0',
    'v0.0.2147483-beta.0',
    'v0.0.2147483',
    'v9007199254740993.0.0',
    'v0.0.0-beta.9007199254740993',
    `v${'9'.repeat(100)}.0.0`,
  ]) {
    assert.throws(() => mapped(tag), /expected .*format|supported Marketplace version range/, tag);
  }
});

test('explicit tags override environment tags and environment tags retain their channel', (context) => {
  const previous = process.env.RHIZOME_RELEASE_TAG;
  context.after(() => {
    if (previous === undefined) delete process.env.RHIZOME_RELEASE_TAG;
    else process.env.RHIZOME_RELEASE_TAG = previous;
  });
  process.env.RHIZOME_RELEASE_TAG = 'v1.2.3-beta.1';
  assert.equal(mapped('v1.2.3').version, '3.2.4000');
  assert.deepEqual(deriveVersion({ fallbackVersion: '0.0.1' }), {
    version: '3.2.3002',
    preRelease: true,
    tag: 'v1.2.3-beta.1',
  });
});

test('untagged local fallback remains available without packaging on import', () => {
  const moduleURL = new URL('./package-platforms.mjs', import.meta.url).href;
  const source = `import { deriveVersion } from ${JSON.stringify(moduleURL)}; console.log(JSON.stringify(deriveVersion({fallbackVersion: '0.0.1'})));`;
  const result = spawnSync(process.execPath, ['--input-type=module', '-e', source], {
    encoding: 'utf8',
    env: { ...process.env, RHIZOME_RELEASE_TAG: '', PATH: '' },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), { version: '0.0.1', preRelease: false, tag: null });
  assert.match(result.stderr, /No git tag resolved/);
});
