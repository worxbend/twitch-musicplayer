/* Tests for docker-entrypoint.d/20-lofi-config.sh and the default
 * public/config.js that static hosts serve.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * The script runs under sh with LOFI_CONFIG_TARGET pointed at a temp file.
 * Accepted values must produce a config.js that parses and round-trips the
 * URL; rejected values must fail without writing any file.
 */
import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const script = fileURLToPath(new URL('../../docker-entrypoint.d/20-lofi-config.sh', import.meta.url));
const defaultConfig = fileURLToPath(new URL('../../public/config.js', import.meta.url));

let tmpDir: string;
let target: string;

beforeEach(() => {
  tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'lofi-config-'));
  target = path.join(tmpDir, 'config.js');
});

afterEach(() => {
  fs.rmSync(tmpDir, { recursive: true, force: true });
});

function runScript(backendUrl: string | undefined) {
  return spawnSync('sh', [script], {
    env: {
      PATH: process.env.PATH,
      LOFI_CONFIG_TARGET: target,
      ...(backendUrl === undefined ? {} : { BACKEND_URL: backendUrl }),
    },
    encoding: 'utf8',
  });
}

function evaluateConfig(code: string, window: Record<string, unknown> = {}): Record<string, unknown> {
  const context = { window };
  vm.runInNewContext(code, context);
  const config = context.window.__LOFI_CONFIG__;
  assert.equal(typeof config, 'object');
  assert.notEqual(config, null);
  // The object comes from another realm, so copy it before deepEqual compares prototypes.
  return { ...(config as Record<string, unknown>) };
}

test('unset BACKEND_URL writes an empty config', () => {
  const run = runScript(undefined);
  assert.equal(run.status, 0, run.stderr);
  const code = fs.readFileSync(target, 'utf8');
  assert.equal(code, 'window.__LOFI_CONFIG__ = {};\n');
  assert.deepEqual(evaluateConfig(code), {});
});

test('empty BACKEND_URL writes an empty config', () => {
  const run = runScript('');
  assert.equal(run.status, 0, run.stderr);
  assert.deepEqual(evaluateConfig(fs.readFileSync(target, 'utf8')), {});
});

for (const url of ['https://radio.example', 'http://[::1]:8080/sub/path?x=1&y=%20']) {
  test(`valid BACKEND_URL round-trips: ${url}`, () => {
    const run = runScript(url);
    assert.equal(run.status, 0, run.stderr);
    assert.deepEqual(evaluateConfig(fs.readFileSync(target, 'utf8')), { backendUrl: url });
  });
}

const hostileUrls = [
  'https://a\nb',
  'https://a\rb',
  'https://a"b',
  'https://a\\b',
  'https://a</script>',
  'https://a b',
  'https://a\tb',
  'javascript:alert(1)',
  'radio.example',
  'https://x";alert(1)//',
];

for (const url of hostileUrls) {
  test(`hostile BACKEND_URL is rejected without writing a file: ${JSON.stringify(url)}`, () => {
    const run = runScript(url);
    assert.notEqual(run.status, 0);
    assert.match(run.stderr, /invalid BACKEND_URL/);
    assert.equal(fs.existsSync(target), false);
  });
}

test('default public/config.js yields an empty config', () => {
  assert.deepEqual(evaluateConfig(fs.readFileSync(defaultConfig, 'utf8')), {});
});

test('default public/config.js keeps a config that is already set', () => {
  const preset = { backendUrl: 'https://x' };
  const code = fs.readFileSync(defaultConfig, 'utf8');
  assert.deepEqual(evaluateConfig(code, { __LOFI_CONFIG__: preset }), preset);
});
