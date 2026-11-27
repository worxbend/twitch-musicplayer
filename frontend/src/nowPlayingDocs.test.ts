/* Drift test: every /now-playing query parameter the overlay parses is documented
 * in the user guide's /now-playing section.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const source = fs.readFileSync(new URL('./nowPlaying.ts', import.meta.url), 'utf8');
const guide = fs.readFileSync(new URL('../../docs/user-guide.md', import.meta.url), 'utf8');

/** The user guide from the /now-playing overlay paragraph up to the next `### ` heading. */
function nowPlayingSection(markdown: string): string {
  const lines = markdown.split('\n');
  const start = lines.findIndex(line => line.includes('`/now-playing` is a text-only overlay'));
  assert.notEqual(start, -1, 'user guide has no /now-playing overlay section');
  const end = lines.findIndex((line, i) => i > start && line.startsWith('### '));
  return lines.slice(start, end === -1 ? undefined : end).join('\n');
}

const parsedParams = [...new Set([...source.matchAll(/params\.get\('([a-z]+)'\)/g)].map(match => match[1]))];

test('the drift test finds every parsed /now-playing parameter', () => {
  // align, size, hold, theme, font, art, next, progress today; fewer means the regex missed some.
  assert.ok(parsedParams.length >= 8, `found only ${parsedParams.join(', ')}`);
});

test('every parsed /now-playing parameter is documented in the user guide', () => {
  const section = nowPlayingSection(guide);
  const missing = parsedParams.filter(name => !new RegExp(`[\`?&]${name}=`).test(section));
  assert.deepEqual(missing, [], `undocumented in docs/user-guide.md: ${missing.join(', ')}`);
});
