const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {verifySanitizer} = require('./prepare-assets.cjs');

test('rejects vulnerable embedded sanitizer even if dependency lock is patched', () => {
  assert.throws(() => verifySanitizer('DOMPurify 3.4.16'), /embedded sanitizer/);
});
test('rejects missing and mixed sanitizer provenance', () => {
  assert.throws(() => verifySanitizer('no sanitizer marker'), /identify/);
  assert.throws(() => verifySanitizer('DOMPurify 3.4.17; DOMPurify 3.4.12'), /embedded sanitizer/);
});
test('accepts the actual pinned browser bundle', () => {
  verifySanitizer(fs.readFileSync(require.resolve('mermaid/dist/mermaid.min.js'), 'utf8'));
});
