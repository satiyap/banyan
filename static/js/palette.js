/* ⌘K: one command bar for the whole desk.
 *
 * A small intent parser — pay, tray, void, report, calibrate, books — that
 * only ever opens a place or fills a form. It never prints, voids or records
 * anything by itself: the action it leads to asks for the same confirmation it
 * always does.
 */
'use strict';

function commandItems(text) {
  const q = fold(text);
  const items = [];
  const add = (label, sub, run, ico) => items.push({ label, sub, run, ico });
  const ents = State.entities;
  const findEnts = t => {
    const f = fold(t);
    if (!f) return [];
    return ents.map(e => {
      const n = fold(entityLabel(e)), p = fold(e.payee_name);
      const s = n === f || p === f ? 3 : n.startsWith(f) || p.startsWith(f) ? 2 : (n + ' ' + p + ' ' + fold(e.category)).includes(f) ? 1 : 0;
      return [s, e];
    }).filter(([s]) => s).sort((a, b) => b[0] - a[0]).slice(0, 5).map(([, e]) => e);
  };

  // pay <entity> [amount]
  let m = /^(?:pay|write)\s+(.+?)(?:\s+(?:rs\.?|₹)?\s*([\d,]+(?:\.\d{1,2})?))?$/.exec(q);
  if (m) {
    const amount = m[2] ? m[2].replace(/,/g, '') : '';
    findEnts(m[1]).forEach(e => add(`Pay ${entityLabel(e)}${amount ? ' ' + rupees(paiseOf(amount), { whole: true }) : ''}`,
      `a filled row in the tray · ${isPerCheque(e) ? 'payee typed per cheque' : 'prints as ' + e.payee_name}`, () => writeInTray({ entity: e, amount }), 'tray'));
    if (!items.length) add(`Write a one-off cheque to “${m[1]}”`, 'a one-off row in the tray', () => writeInTray({ oneOff: true, payee: m[1], amount }), 'pen');
  }
  // void <leaf>
  m = /^(?:void|stop)\s+([\w-]+)$/.exec(q);
  if (m) add(`Void leaf ${m[1]}…`, 'opens it in the register with the void form — asks for a reason first', () => go('register', { leaf: m[1], void: 1 }), 'x');
  // a bare leaf number
  m = /^\d{3,}$/.exec(q);
  if (m) add(`Open leaf ${q}`, 'in the register', () => go('register', { leaf: q }), 'register');
  // reports
  m = /^(?:(q[1-4])\s*)?(?:report|fy)?\s*(?:fy\s*)?(\d{4})?(?:\s*report)?$/.exec(q);
  if (q && m && (m[1] || m[2] || /report|year/.test(q))) {
    const fy = m[2] ? Number(m[2]) : fyOf(todayISO());
    const quarter = m[1] ? Number(m[1][1]) : 0;
    add(`${quarter ? `Q${quarter} ` : ''}${fyLabel(fy)} report`, 'the Year view', () => go('year', { fy, q: String(quarter) }), 'year');
  }
  if (/^tray/.test(q) || /all due|everything due/.test(q)) {
    add('Tray everything due this week', 'adds each overdue or due entity once, with its usual amount', () => { trayAllDue(); go('tray'); }, 'tray');
    add('Open the tray', '', () => go('tray'), 'tray');
  }
  if (/test|sheet/.test(q)) add('Print a test sheet…', 'the diagnostic sheet, on plain paper — asks first', printTestSheet, 'printer');
  if (/cal/.test(q)) add('Calibration', 'version, nudges and fields — and the test sheet', openCalibration, 'leaf');
  if (/book/.test(q)) {
    add('Cheque books', 'the books, how far each has got, the 80% alert', () => go('register', { sub: 'books' }), 'book');
    add('Add a cheque book…', '', () => go('register', { sub: 'books', add: 1 }), 'plus');
  }
  if (/entit|new|add/.test(q)) add('New entity…', '', () => editEntity(null), 'plus');

  // plain entity names
  if (q && !/^(pay|write|void|stop)\b/.test(q)) {
    findEnts(text).forEach(e => add(`Write ${entityLabel(e)}`, `${isPerCheque(e) ? 'payee typed per cheque' : 'prints as ' + e.payee_name}${usualPaise(e) ? ' · usually ' + rupees(usualPaise(e), { whole: true }) : ''}`,
      () => writeInTray({ entity: e }), 'tray'));
  }
  // places
  [['desk', 'Desk'], ['tray', 'Tray'], ['register', 'Register'], ['year', 'Year'], ['monthly', 'Monthly']].forEach(([k, l]) => {
    if (!q || fold(l).startsWith(q) || q === 'go ' + k) add(`Go to ${l}`, '', () => go(k), k);
  });
  if (!q) {
    add('Tray everything due this week', 'T on the Desk', () => { trayAllDue(); go('tray'); }, 'tray');
    add('Print a test sheet…', 'on plain paper', printTestSheet, 'printer');
    add('Calibration', '', openCalibration, 'leaf');
  }
  return items.slice(0, 9);
}

let paletteOpen = false;
async function openPalette() {
  if (paletteOpen) return;
  paletteOpen = true;
  const input = h('input.palette-input', { type: 'text', placeholder: '“pay power 67865”, “void 004417”, “Q2 report”, “calibrate”…', 'aria-label': 'Command', autocomplete: 'off' });
  const list = h('div.palette-list', { role: 'listbox' });
  let items = [], k = 0, chosen = null;
  const paint = () => {
    items = commandItems(input.value);
    k = Math.min(k, Math.max(0, items.length - 1));
    clear(list, items.map((it, i) => {
      const n = h('div.palette-item' + (i === k ? '.is-active' : ''), { role: 'option', 'aria-selected': String(i === k) },
        icon(it.ico || 'search'), h('div', h('div.strong', it.label), it.sub ? h('div.sub', it.sub) : null));
      n.addEventListener('mousedown', ev => { ev.preventDefault(); chosen = it; $('dialog.palette').close('go'); });
      return n;
    }));
    if (!items.length) list.appendChild(h('p.empty', 'Nothing matches. Try “pay”, “void”, “report”, “tray”, “books” or a name.'));
  };
  input.addEventListener('input', () => { k = 0; paint(); });
  input.addEventListener('keydown', ev => {
    if (ev.key === 'ArrowDown') { ev.preventDefault(); k = Math.min(items.length - 1, k + 1); paint(); }
    else if (ev.key === 'ArrowUp') { ev.preventDefault(); k = Math.max(0, k - 1); paint(); }
    else if (ev.key === 'Enter') { ev.preventDefault(); chosen = items[k]; $('dialog.palette').close('go'); }
  });
  paint();
  const res = await modal({ className: 'palette', body: h('div', input, list, h('div.palette-foot', '↑↓ choose · Enter open · esc close · nothing is printed or saved from here')),
                            onOpen: () => input.focus() });
  paletteOpen = false;
  if (res === 'go' && chosen) chosen.run();
}

document.addEventListener('keydown', ev => {
  if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'k') { ev.preventDefault(); openPalette(); return; }
  const typing = document.activeElement && /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName);
  if (typing || $$('dialog').some(d => d.open) || ev.metaKey || ev.ctrlKey || ev.altKey) return;
  if (ev.key === '/') { ev.preventDefault(); openPalette(); }
  if (Router.place === 'desk' && (ev.key === 't' || ev.key === 'T')) { ev.preventDefault(); trayAllDue(); }
});

/* ------------------------------------------------------------- test sheet */
async function printTestSheet() {
  const blocked = healthBlocksPrint();
  const choice = await modal({
    title: 'Print the diagnostic test sheet',
    body: h('div', h('p', 'Grid, rulers, a 100 mm reference line and every field box, at the measured geometry. Print it on plain paper and hold it over a real leaf against the light — never on the leaf itself.'),
      blocked ? h('p.form-error', blocked) : loadingNote('Load a sheet of plain paper the way a leaf goes in: face up, sideways, Pay end first.')),
    actions: [{ label: 'Cancel', value: 'cancel' },
              { label: 'Download the PDF', value: 'pdf' },
              { label: 'Print it', primary: true, value: 'print', disabled: !!blocked }],
  });
  if (!choice || choice === 'cancel') return;
  try {
    const res = await post('/api/test-print', { printer: State.printer, confirm: choice === 'print', pattern: 'test' });
    if (choice === 'pdf') window.open(res.pdf, '_blank', 'noopener');
    else toast(`Test sheet sent — ${res.page.page_w_mm}×${res.page.page_h_mm} mm. Hold it against a real leaf.`);
  } catch (err) { toast(err.message, true); }
}

/* ------------------------------------------------------------- calibration */
async function openCalibration() {
  const cal = State.cal;
  const hh = State.health;
  if (!cal) return;
  const view = makeLeaf({ small: true });
  view.boxesOn = true;
  view.draw({ payee: 'Fictitious Payee Name', cheque_date: todayISO(), amount_words: 'Twelve Thousand Three Hundred Forty-Five Rupees Only',
              amount_figures: '12,345.00/-', crossing: true });
  view.showBoxes(true);
  const F = cal.fields;
  const fields = Object.entries(F).map(([k, f]) => [k, Object.entries(f).map(([a, b]) => `${a} ${b}`).join(' · ')]);
  await modal({
    eyebrow: `${cal.source} · version ${cal.version}`, title: 'Calibration', wide: true,
    body: h('div',
      hh && hh.calibration.error ? h('p.form-error', `The file is broken and the last good copy is in use: ${hh.calibration.error}`) : null,
      h('div.mat.mat-small', view.el),
      dl([
        ['Global nudge', `dx ${cal.dx} mm (prints as page y) · dy ${cal.dy} mm (prints as page −x)`],
        ['Feed', cal.flip ? 'flipped 180°' : 'normal'],
        ['Paper', cal.paper === 'a4' ? `A4, block offset ${cal.a4x} × ${cal.a4y} mm` : 'exact 92 × 204 mm page'],
        ['Printable limit', `${cal.hardware_inset_mm} mm on every edge (M126nw)`],
        ['MICR band', `from y = ${cal.micr_top_mm} mm — nothing is printed there`],
      ]),
      cal.warnings.length ? h('ul.checks', cal.warnings.map(w => h('li.warn', icon('alert'), w))) : null,
      h('details', h('summary', 'Field geometry (mm from the leaf’s top-left)'), dl(fields)),
      h('p.sub', 'Measurements live in cheque-calibration.json and nowhere else. Edit that file after a plain-paper test — the desk picks the change up on its own and re-lays every leaf on this page, ink and inputs together. Bump the version with every change; each cheque records the version it was printed under. Run verify-print.py after any change to the renderer.')),
    actions: [{ label: 'Close', value: 'close' }, { label: 'Print a test sheet…', primary: true, value: 'test', onClick: (ev, dlg) => { ev.preventDefault(); dlg.close('test'); setTimeout(printTestSheet, 50); } }],
  });
}

/* ------------------------------------------------------------- printer */
async function openHealth() {
  const hh = State.health;
  if (!hh) { toast('The server is not answering.', true); return; }
  const sel = h('select', { 'aria-label': 'Printer' }, hh.printing.printers.map(p =>
    h('option', { value: p.name, selected: printerOf(hh) && printerOf(hh).name === p.name },
      p.name.replace(/_/g, ' ') + (p.online === false ? ' (offline)' : p.state === 'idle' ? '' : ` (${p.state})`))));
  sel.addEventListener('change', () => { State.printer = sel.value; store('banyan.printer', sel.value); loadHealth(); });
  await modal({
    eyebrow: 'Printer & health', title: hh.ok ? 'Ready to print' : 'Needs attention',
    body: h('div',
      hh.printing.printers.length ? field('Print to', sel) : h('p.form-error', 'No printer found.'),
      dl([
        ['Renderer', hh.renderer.ok ? 'Ready' : hh.renderer.path, hh.renderer.ok ? '' : 'bad'],
        ['Print system', hh.printing.cups ? 'CUPS ready' : 'CUPS unavailable', hh.printing.cups ? '' : 'bad'],
        ['Calibration', `${hh.calibration.source} v${hh.calibration.version}`, hh.calibration.error ? 'bad' : ''],
        hh.calibration.error ? ['Calibration error', hh.calibration.error, 'bad'] : null,
        ['Page', `${hh.page_mm.w} × ${hh.page_mm.h} mm, fed sideways`],
        ['Register', hh.db],
        ['Desk version', hh.desk_version],
      ])),
    actions: [{ label: 'Calibration', value: 'cal', onClick: (ev, dlg) => { ev.preventDefault(); dlg.close('cal'); setTimeout(openCalibration, 50); } },
              { label: 'Close', primary: true, value: 'close' }],
  });
}
