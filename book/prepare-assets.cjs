const fs = require('node:fs');
const path = require('node:path');
const destination = path.join(__dirname, 'src/vendor');
fs.mkdirSync(destination, {recursive: true});
fs.copyFileSync(require.resolve('mermaid/dist/mermaid.min.js'), path.join(destination, 'mermaid.min.js'));
const license = path.join(__dirname, 'node_modules/mermaid/LICENSE');
if (fs.existsSync(license)) fs.copyFileSync(license, path.join(destination, 'mermaid.LICENSE'));
