// Test the exact served bundle with Chromium. All non-fixture HTTP is blocked.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFileSync, readdirSync, existsSync} from 'node:fs';
import {mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), 'build');
const htmlFiles = directory => readdirSync(directory, {withFileTypes: true}).flatMap(entry => {
  const file = path.join(directory, entry.name);
  return entry.isDirectory() ? htmlFiles(file) : file.endsWith('.html') ? [file] : [];
});
const pages = htmlFiles(root).filter(file => readFileSync(file, 'utf8').includes('class="language-mermaid"'));
assert.ok(pages.length, 'The real book must contain diagrams');
const server = createServer((request, response) => {
  const relative = decodeURIComponent(new URL(request.url, 'http://localhost').pathname).replace(/^\/pulse\//, '/');
  const file = path.resolve(root, '.' + relative);
  if (!file.startsWith(root + path.sep) || !existsSync(file)) {response.writeHead(404).end(); return;}
  const types = {'.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml'};
  response.setHeader('Content-Type', types[path.extname(file)] || 'application/octet-stream');
  response.end(readFileSync(file));
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
const profile = await mkdtemp(path.join(tmpdir(), 'pulse-browser-security-'));
const debugPort = Number(process.env.CHROME_DEBUG_PORT || 19479);
const chrome = spawn(process.env.CHROME_BIN || (existsSync('/usr/bin/chromium') ? '/usr/bin/chromium' : 'google-chrome'), [
  '--headless', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', '--disable-background-networking',
  '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${debugPort}`,
  `--user-data-dir=${profile}`, 'about:blank',
], {stdio: 'ignore'});
let socket;
try {
  let targets;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {targets = await fetch(`http://127.0.0.1:${debugPort}/json/list`).then(r => r.json()); break;} catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.ok(targets?.some(target => target.type === 'page'), 'Chromium did not start');
  socket = new WebSocket(targets.find(target => target.type === 'page').webSocketDebuggerUrl);
  await new Promise(resolve => socket.addEventListener('open', resolve, {once: true}));
  let id = 0;
  const pending = new Map();
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    pending.set(++id, {resolve, reject});
    socket.send(JSON.stringify({id, method, params}));
  });
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (message.id) {
      const request = pending.get(message.id); pending.delete(message.id);
      if (message.error) request.reject(Error(JSON.stringify(message.error))); else request.resolve(message.result);
    } else if (message.method === 'Fetch.requestPaused') {
      const {requestId, request} = message.params;
      const allowed = request.url.startsWith(origin + '/') || request.url.startsWith('data:');
      send(allowed ? 'Fetch.continueRequest' : 'Fetch.failRequest', allowed ? {requestId} : {requestId, errorReason: 'BlockedByClient'}).catch(() => {});
    }
  });
  const evaluate = async expression => {
    const result = await send('Runtime.evaluate', {expression, returnByValue: true, awaitPromise: true});
    if (result.exceptionDetails) throw Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
    return result.result.value;
  };
  const wait = async expression => {
    for (let attempt = 0; attempt < 200; attempt++) {
      try {if (await evaluate(expression)) return;} catch (error) {
        if (!/Execution context was destroyed|Cannot find context/.test(String(error))) throw error;
      }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw Error('Timed out rendering book diagrams');
  };
  await send('Page.enable');
  await send('Fetch.enable', {patterns: [{urlPattern: '*'}]});
  for (const page of pages) {
    const url = origin + '/pulse/' + path.relative(root, page).split(path.sep).join('/');
    await send('Page.navigate', {url});
    await wait(`location.href===${JSON.stringify(url)} && document.readyState==='complete' && document.querySelectorAll('.pulse-diagram svg').length>0 && !document.querySelector('code.language-mermaid') && !document.documentElement.dataset.diagramError`);
  }
  const before = await evaluate(`document.querySelector('.pulse-diagram svg').id`);
  await evaluate(`document.documentElement.classList.add('navy')`);
  await wait(`document.querySelector('.pulse-diagram svg').id!==${JSON.stringify(before)} && !document.documentElement.dataset.diagramError`);
  const dark = await evaluate(`document.querySelector('.pulse-diagram svg').id`);
  await evaluate(`document.documentElement.classList.remove('navy')`);
  await wait(`document.querySelector('.pulse-diagram svg').id!==${JSON.stringify(dark)} && !document.documentElement.dataset.diagramError`);
  const security = await evaluate(`(async()=>{
    window.__pulseXss=false;
    const vectors=[
      'flowchart LR\\n A["<img src=x onerror=window.__pulseXss=true>"] --> B[Safe]',
      'flowchart LR\\n A[Safe] --> B[Test]\\n click A "javascript:window.__pulseXss=true"'
    ];
    for(let i=0;i<vectors.length;i++){
      mermaid.initialize({startOnLoad:false,securityLevel:'strict'});
      const {svg}=await mermaid.render('security-fixture-'+i,vectors[i]);
      if(/<script|onerror\\s*=|onload\\s*=|javascript:/i.test(svg))throw Error('Unsafe output from bundled sanitizer');
      const node=document.createElement('div');node.innerHTML=svg;document.body.append(node);
    }
    await new Promise(resolve=>setTimeout(resolve,100));
    return !window.__pulseXss;
  })()`);
  assert.equal(security, true, 'Injected browser code executed');
  console.log(`PASS: ${pages.length} real diagram pages, dark/light rerender, two malicious diagram vectors; external HTTP blocked`);
} finally {
  socket?.close(); chrome.kill();
  await new Promise(resolve => chrome.once('exit', resolve));
  await new Promise(resolve => server.close(resolve));
  await rm(profile, {recursive: true, force: true, maxRetries: 10, retryDelay: 100});
}
