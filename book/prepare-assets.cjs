const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

function verifySanitizer(bundle) {
  const versions = [...bundle.matchAll(/DOMPurify (\d+\.\d+\.\d+)/g)].map(match => match[1]);
  assert.ok(versions.length > 0, 'Mermaid bundle must identify its embedded DOMPurify version');
  assert.deepEqual([...new Set(versions)], ['3.4.17'], 'Review embedded sanitizer before changing the pinned Mermaid distribution');
}

module.exports = {verifySanitizer};
if (require.main !== module) return;
const destination = path.join(__dirname, 'src/vendor');
fs.mkdirSync(destination, {recursive: true});
const bundle = fs.readFileSync(require.resolve('mermaid/dist/mermaid.min.js'), 'utf8');
verifySanitizer(bundle);
fs.writeFileSync(path.join(destination, 'mermaid.min.js'), bundle);
const license = path.join(__dirname, 'node_modules/mermaid/LICENSE');
if (fs.existsSync(license)) fs.copyFileSync(license, path.join(destination, 'mermaid.LICENSE'));
