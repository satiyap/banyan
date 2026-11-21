// Mac App Store screenshots: each place of the desk under a headline, at
// 2880 × 1800 (16:10, the store's largest Mac size).
//
//   node scripts/appstore-screenshots.mjs http://127.0.0.1:18456 build/appstore/screenshots
//
// Point it at a development desk (`go run ./cmd/deskdev -data <scratch folder>`)
// holding an invented register: every name and amount in the screenshots must
// be fictitious. It drives an installed Google Chrome over the DevTools
// protocol; nothing is printed and nothing is saved.
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const desk = (process.argv[2] || 'http://127.0.0.1:18456').replace(/\/$/, '');
const out = resolve(process.argv[3] || 'build/appstore/screenshots');
const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const W = 1440, H = 900, SCALE = 2;
const sleep = ms => new Promise(r => setTimeout(r, ms));

// The places, in the order the store shows them, with the work that sets each one up.
const shots = [
  { file: '01-desk', hash: '#/desk', title: 'Every payment, on one desk.',
    sub: 'What is due this fortnight, what was paid, and everyone you pay.' },
  { file: '02-tray', hash: '#/desk', title: 'Prints onto your real cheque leaf.',
    sub: 'Date, payee, amount in words and figures and the crossing, each in its measured place.',
    // The leaf photo's printed branch address and IFSC are blurred in the
    // public screenshot: they would say where the organisation banks.
    prep: `[...document.querySelectorAll('button')].filter(b => b.textContent.trim() === 'Write')
             .find(b => b.closest('div').parentElement.textContent.includes('Caretaker')).click();
           await sleep(1200);
           const leaf = document.querySelector('.leaf.has-photo');
           if (leaf) leaf.appendChild(Object.assign(document.createElement('div'), { style:
             'position:absolute;left:71mm;top:1mm;width:57mm;height:13mm;backdrop-filter:blur(3.5px);-webkit-backdrop-filter:blur(3.5px);z-index:5' }));`,
    last: true },
  { file: '03-register', hash: '#/register', title: 'A register of every leaf.',
    sub: 'Printed, handwritten or voided, book by book. Nothing is ever deleted.' },
  { file: '04-year', hash: '#/year', title: 'The year, already added up.',
    sub: 'April to March, by category, quarter and payee. Open any row to see its cheques.' },
  { file: '05-category', hash: '#/year', title: 'Change a category or a note in place.',
    sub: 'See what a change moves before you save it, and undo it with one click.',
    prep: `[...document.querySelectorAll('.ann-toggle')].find(b => b.textContent.includes('Lift AMC')).click();
           await sleep(300);
           document.querySelector('.ann-toggle[aria-expanded=true]').scrollIntoView({ block: 'center' });
           await sleep(200);
           document.querySelectorAll('.ann-leaf')[0].click();
           await sleep(300);
           const misc = [...document.querySelectorAll('.ann-option')].find(l => l.textContent.trim() === 'Misc');
           misc.querySelector('input').click();
           const note = document.querySelector('.ann-note-field');
           note.value = 'Includes the one-time cable replacement.';
           note.dispatchEvent(new Event('input', { bubbles: true }));` },
  { file: '06-monthly', hash: '#/monthly', title: 'A report the committee can read.',
    sub: 'Every month by category, as a PDF to print or send.' },
];

// -- a small DevTools client --------------------------------------------------
async function devtools(port) {
  for (let i = 0; i < 50; i++) {
    try { return await (await fetch(`http://127.0.0.1:${port}/json/version`)).json(); } catch { await sleep(200); }
  }
  throw new Error('Chrome did not start');
}

function connect(url) {
  const ws = new WebSocket(url);
  let id = 0;
  const waiting = new Map();
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && waiting.has(m.id)) {
      const { ok, fail } = waiting.get(m.id);
      waiting.delete(m.id);
      m.error ? fail(new Error(m.error.message)) : ok(m.result);
    }
  };
  const send = (method, params = {}, sessionId) => new Promise((ok, fail) => {
    const n = ++id;
    waiting.set(n, { ok, fail });
    ws.send(JSON.stringify({ id: n, method, params, sessionId }));
  });
  return new Promise(ok => { ws.onopen = () => ok({ send, close: () => ws.close() }); });
}

async function page(cdp, w, h, scale) {
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  const s = (m, p) => cdp.send(m, p, sessionId);
  await s('Page.enable');
  await s('Runtime.enable');
  await s('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: scale, mobile: false });
  const run = async expr => {
    const r = await s('Runtime.evaluate', { expression: `(async () => { const sleep = ms => new Promise(r => setTimeout(r, ms)); ${expr} })()`, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  };
  return {
    go: async url => { await s('Page.navigate', { url }); await sleep(800); },
    run,
    shot: async () => Buffer.from((await s('Page.captureScreenshot', { format: 'png' })).data, 'base64'),
    close: () => cdp.send('Target.closeTarget', { targetId }),
  };
}

// -- the frame around each screenshot --------------------------------------------
const fonts = pathToFileURL(resolve('static/fonts')).href;
function frame(title, sub, png) {
  return `<!doctype html><html><head><meta charset="utf-8"><style>
@font-face { font-family: Hedvig; src: url("${fonts}/hedvig-letters-serif-latin-400-normal.woff2"); }
@font-face { font-family: Geist; src: url("${fonts}/geist-sans-latin-400-normal.woff2"); font-weight: 400; }
@font-face { font-family: Geist; src: url("${fonts}/geist-sans-latin-500-normal.woff2"); font-weight: 500; }
html, body { margin: 0; width: 2880px; height: 1800px; overflow: hidden; }
body { background: oklch(94% 0.009 215); color: oklch(16% 0.032 250); font-family: Geist, sans-serif;
  display: flex; flex-direction: column; align-items: center; }
header { text-align: center; padding-top: 112px; }
h1 { font: 400 104px/1.05 Hedvig, serif; letter-spacing: -0.02em; margin: 0; text-wrap: balance; }
p { font-size: 42px; line-height: 1.35; color: oklch(34% 0.022 245); margin: 30px auto 0; max-width: 1900px; text-wrap: balance; }
.shot { margin-top: 84px; width: 2240px; height: 1400px; border-radius: 22px; overflow: hidden;
  box-shadow: 0 50px 110px -40px oklch(16% 0.032 250 / 0.45), 0 0 0 2px oklch(84% 0.010 225); }
.shot img { width: 100%; display: block; }
</style></head><body><header><h1>${title}</h1><p>${sub}</p></header>
<div class="shot"><img src="data:image/png;base64,${png.toString('base64')}"></div></body></html>`;
}

// -- run --------------------------------------------------------------------------
const profile = mkdtempSync(join(tmpdir(), 'banyan-shots-'));
const port = 9300 + Math.floor(Math.random() * 500);
const proc = spawn(chrome, ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  '--hide-scrollbars', '--no-first-run', '--no-default-browser-check', 'about:blank'], { stdio: 'ignore' });
try {
  const { webSocketDebuggerUrl } = await devtools(port);
  const cdp = await connect(webSocketDebuggerUrl);
  mkdirSync(out, { recursive: true });
  const work = mkdtempSync(join(tmpdir(), 'banyan-frames-'));
  // The Tray shot leaves a cheque in the tray, so it is taken last.
  for (const shot of [...shots.filter(x => !x.last), ...shots.filter(x => x.last)]) {
    const p = await page(cdp, W, H, SCALE);
    await p.go(`${desk}/${shot.hash}`);
    // The desk waits for its first printer check before it draws.
    for (let i = 0; i < 40 && !(await p.run(`return !!document.querySelector('#content') && document.querySelector('#content').children.length > 0 && !document.querySelector('#content .empty')`)); i++) await sleep(250);
    // No printer answers a scratch desk; its chip would only say so.
    await p.run(`const t = document.getElementById('printerText'); if (t) t.closest('button, .chip, div').style.visibility = 'hidden';`);
    if (shot.prep) { await p.run(shot.prep); await sleep(900); }
    // A click leaves a keyboard focus ring; a screenshot should not show one.
    await p.run(`if (document.activeElement && !document.querySelector('dialog[open]')) document.activeElement.blur();`);
    const png = await p.shot();
    await p.close();
    const html = join(work, `${shot.file}.html`);
    writeFileSync(html, frame(shot.title, shot.sub, png));
    const f = await page(cdp, 2880, 1800, 1);
    await f.go(pathToFileURL(html).href);
    await f.run(`await document.fonts.ready; await sleep(300);`);
    writeFileSync(join(out, `${shot.file}.png`), await f.shot());
    await f.close();
    console.log('wrote', `${shot.file}.png`);
  }
  rmSync(work, { recursive: true, force: true });
  cdp.close();
} finally {
  proc.kill();
  await sleep(300);
  rmSync(profile, { recursive: true, force: true });
}
