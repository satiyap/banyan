/* The Tray: what goes to the printer.
 *
 * Before a run, the tray is a grid of draft cheques — one row each, filled
 * from an entity (its payee, usual amount, head and date rule) or created in
 * place. Drafts live in this browser, as they always have.
 *
 * A run is the existing batch API, step for step, and nothing else:
 *   first leaf alone → its own verdict → the rest → one verdict for all of
 *   them, marking only the leaves that came out wrong. The first leaf is the
 *   check, so a plain-paper test (nothing recorded) is offered, not asked for:
 *   it earns its keep after a recalibration or on a new printer path.
 * A single cheque is not a batch: it prints through POST /api/cheques and
 * gets one Printed / Misprinted.
 */
'use strict';

const TrayPage = {
  cur: 0,
  creating: null,        // { index, typed } while the New entity panel is open
  single: null,          // { id, cheque_no, index, draft } a single cheque at the printer
  finished: null,        // { batch, leftovers } after a run ends
  misprinted: new Set(),
  ticks: new Set(),
  testWhich: 'first',
  busy: false,
  firstNo: '',
  testsPrinted: 0,
};

const CHECKLIST = [
  'Date digits sit inside the eight DDMMYYYY boxes',
  'Payee starts after PAY and ends before OR ORDER',
  'Words start after RUPEES on the first ruled line',
  'Amount sits right of the ₹ cell and ends in /-',
  'Crossing lands at the top-left corner',
  'Nothing below the ‘MULTI-CITY CHEQUE’ line (MICR band)',
];

function runStage(b) {
  if (!b) return null;
  if (b.status === 'review') return stored(`banyan.run.${b.id}`, 'first');
  return b.status;
}
function setRunStage(b, s) { store(`banyan.run.${b.id}`, s); }

Places.tray = {
  title: 'Tray',
  render(root) {
    const desk = State.desk;
    if (!desk) { root.appendChild(h('p.empty', 'Loading…')); return; }
    if (TrayPage.single) { renderSingle(root); return; }
    if (desk.batch) { renderRun(root, desk.batch); return; }
    renderGrid(root);
  },
  refresh() {
    const a = document.activeElement;
    if (a && $('#content').contains(a) && /INPUT|TEXTAREA|SELECT/.test(a.tagName)) return;
    route();
  },
};

/* ------------------------------------------------------------- grid */
function isPerCheque(e) { return !!e && e.kind === 'per_cheque'; }

function rowState(d) {
  if (d.entity_id != null && entityById(d.entity_id)) {
    const e = entityById(d.entity_id);
    if (isPerCheque(e) && (!d.payee.trim() || d.payee.trim() === e.payee_prefix)) return { entity: e, problem: 'needs payee' };
    return { entity: e };
  }
  if (d.one_off) return d.payee.trim() ? { oneOff: true } : { problem: 'needs payee' };
  const m = entitiesPrinting(d.payee);
  if (!d.payee) return { problem: 'needs entity' };
  if (m.length === 1) return { entity: m[0] };
  if (m.length > 1) return { problem: 'pick which entity' };
  return { fresh: true };
}

function blankRow(extra) {
  return Object.assign({ payee: '', entity_id: null, amount: '', amount_paise: 0, cheque_date: todayISO(), memo: '',
                         crossing: true, strike_order: false, star_fill: false, one_off: false }, extra || {});
}

function renderGrid(root) {
  const P = TrayPage;
  const rows = State.tray;
  P.cur = Math.min(P.cur, Math.max(0, rows.length - 1));
  const head = h('div.page-head',
    h('div',
      h('div.eyebrow', rows.length ? `The tray · ${plural(rows.length, 'cheque')}` : 'The tray is empty'),
      h('h2.headline', rows.length > 1 ? `Write ${countWord(rows.length, true)} cheques at once.` : rows.length ? 'One cheque in the tray.' : 'Nothing to print yet.'),
      h('p.lede', 'Each row is one cheque: pick an entity and its payee, amount and date fill in. The leaf shows the row you are on.')));
  // What adds rows sits on the rows, not in the page heading.
  const bar = h('div.grid-bar',
    h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: addRow }, icon('plus'), 'Add row'),
    h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: addOneOff }, icon('pen'), 'One-off cheque'),
    h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: pasteRows }, icon('doc'), 'Paste from sheet'),
    h('span.tool-push'),
    h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: () => openCreate(null, '') }, icon('plus'), 'New entity'));

  const finished = P.finished ? finishedCard() : null;
  const table = h('div.grid', { role: 'table', 'aria-label': 'Cheques in the tray' },
    h('div.grid-row.grid-head', { role: 'row' },
      ['', 'Entity', 'Amount', 'Cheque date', 'A/C', 'Note', 'Check', ''].map(t => h('div', { role: 'columnheader' }, t))));
  rows.forEach((d, i) => table.appendChild(gridRow(d, i)));
  if (!rows.length) {
    table.appendChild(h('div.grid-empty', 'Add cheques from the Desk (tick chips, or press T for everything due), or add a row or a one-off here.'));
  }
  // The leaf on the left; beside it, everything about acting on it: who it
  // pays, the print decision, and (folded) the measured field positions.
  // The rows follow straight after, at the same width, so typing is in view.
  const stage = rows.length || P.creating
    ? h('div.tray-stage' + (P.creating ? '.is-creating' : ''),
        h('div.tray-col', h('div.tray-leaf')),
        h('div.tray-side',
          h('aside.tray-memory'),
          gridFoot(),
          h('details.cx-fold', h('summary', 'Field positions'), h('div.cx-slot')))) : null;
  appendTo(root, head, finished, stage, bar, h('div.tray-grid-wrap', table, stage ? null : gridFoot()));
  paintStage();
  rows.forEach((d, i) => checkRow(d, i));
}

function gridRow(d, i) {
  const P = TrayPage;
  const st = rowState(d);
  const e = st.entity;
  const perCheque = isPerCheque(e);
  const row = h('div.grid-row' + (i === P.cur ? '.is-current' : '') + (perCheque ? '.has-payee' : ''), { role: 'row', dataset: { i: String(i) } });
  row.addEventListener('focusin', () => selectRow(i));
  row.addEventListener('click', () => selectRow(i));

  const oneOff = d.one_off && d.entity_id == null;
  const name = h('input.cell-entity', { type: 'text', value: e ? entityLabel(e) : (d._typed != null ? d._typed : d.payee || ''),
    placeholder: oneOff ? 'payee, exactly as printed' : 'type to find or create', 'aria-label': `Row ${i + 1} ${oneOff ? 'payee' : 'entity'}`, autocomplete: 'off' });
  const sub = h('div.cell-sub' + (oneOff || perCheque ? '.tone-kind' : ''),
    perCheque ? 'payee per cheque' : oneOff ? 'one-off · no entity' : e ? `pays ${e.payee_name}` : st.fresh ? `new payee · ${d.payee}` : 'type to find or create');
  name.addEventListener('focus', () => openPicker(name, i));
  name.addEventListener('input', () => {
    d._typed = name.value;
    if (oneOff) { d.payee = name.value.split(/\s+/).filter(Boolean).join(' '); saveTrayQuiet(); checkRow(d, i); paintLeafQuiet(); }
    openPicker(name, i);
  });
  name.addEventListener('keydown', ev => pickerKeys(ev, i));
  name.addEventListener('blur', () => setTimeout(() => closePicker(), 160));

  const amount = h('input.cell-amt.mono', { type: 'text', inputmode: 'decimal', placeholder: 'Amount', value: d.amount || '', 'aria-label': `Row ${i + 1} amount` });
  amount.addEventListener('input', () => { d.amount = amount.value.replace(/[^\d.,]/g, ''); d.amount_paise = paiseOf(d.amount) || 0; saveTrayQuiet(); checkRow(d, i); paintLeafQuiet(); });
  const date = h('input.cell-date', { type: 'date', value: d.cheque_date || '', 'aria-label': `Row ${i + 1} cheque date` });
  date.addEventListener('change', () => { d.cheque_date = date.value; saveTrayQuiet(); checkRow(d, i); paintFootQuiet(); paintLeafQuiet(); });
  const ac = h('input', { type: 'checkbox', checked: d.crossing !== false, 'aria-label': `Row ${i + 1} A/C payee crossing` });
  ac.addEventListener('change', () => { d.crossing = ac.checked; saveTrayQuiet(); checkRow(d, i); paintStage(); });
  const memo = h('input.cell-note', { type: 'text', placeholder: 'Note', value: d.memo || '', maxlength: '200', 'aria-label': `Row ${i + 1} note` });
  memo.addEventListener('input', () => { d.memo = memo.value; saveTrayQuiet(); });
  [amount, memo].forEach(inp => inp.addEventListener('keydown', ev => moveKeys(ev, i)));

  appendTo(row,
    h('div.cell-n.mono', String(i + 1)),
    h('div.cell-ent', h('div.ent-input', name, icon('chevrons')), sub),
    h('div', amount),
    h('div.cell-datewrap', date, d.cheque_date > todayISO() ? h('span.tag.tone-pdc', 'PDC') : null),
    // The column heading names the crossing on a wide screen; a stacked row
    // has no headings, so it carries its own label there.
    h('label.cell-ac', ac, h('span.cell-ac-label', 'A/C payee')),
    h('div', memo),
    h('div.cell-check', h('span.check.muted', '…')),
    h('div', h('button.icon-btn', { type: 'button', 'aria-label': `Remove row ${i + 1}`, title: 'Remove from the tray',
      onclick: () => { State.tray.splice(i, 1); saveTray(); route(); } }, icon('x'))),
    perCheque ? payeeLine(d, i, e) : null);
  return row;
}

/* A payee-per-cheque entity's row carries its own Pay line: the prefix comes
   prefilled and the rest is typed, or the whole thing overwritten. */
function payeeLine(d, i, e) {
  const listId = `payees-${i}`;
  const inp = h('input.cell-payee.mono', { type: 'text', value: d.payee || '', maxlength: '120', list: listId,
    placeholder: e.payee_prefix ? `${e.payee_prefix} …` : 'payee, exactly as printed', 'aria-label': `Row ${i + 1} payee on the leaf`, autocomplete: 'off' });
  const note = h('span.sub');
  const paintNote = () => {
    const p = inp.value.trim();
    note.textContent = !p || p === e.payee_prefix ? (e.payee_prefix ? `type the rest after “${e.payee_prefix}”` : 'type who this cheque is for')
      : (e.payees || []).includes(p) ? `paid before as ${entityLabel(e)}` : `new payee · still ${entityLabel(e)}`;
  };
  inp.addEventListener('input', () => { d.payee = inp.value.split(/\s+/).filter(Boolean).join(' '); paintNote(); saveTrayQuiet(); checkRow(d, i); paintLeafQuiet(); });
  inp.addEventListener('keydown', ev => moveKeys(ev, i));
  paintNote();
  return h('div.grid-payee',
    h('label.eyebrow', { for: inp.id = `payee-in-${i}` }, 'Payee on the leaf'), inp, note,
    h('datalist', { id: listId }, (e.payees || []).map(p => h('option', { value: p }))));
}

function selectRow(i) {
  const P = TrayPage;
  if (P.cur === i) return;
  P.cur = i;
  $$('.grid-row[data-i]').forEach(r => r.classList.toggle('is-current', Number(r.dataset.i) === i));
  paintStage();
}

function moveKeys(ev, i) {
  if (ev.key !== 'ArrowDown' && ev.key !== 'ArrowUp') return;
  const j = i + (ev.key === 'ArrowDown' ? 1 : -1);
  const rows = $$('.grid-row[data-i]');
  if (!rows[j]) return;
  ev.preventDefault();
  const cls = ['.cell-note', '.cell-payee'].find(c => ev.target.matches(c)) || '.cell-amt';
  ($(cls, rows[j]) || $('.cell-amt', rows[j])).focus();
}

let trayQuietTimer;
function saveTrayQuiet() { State.dirty = true; clearTimeout(trayQuietTimer); trayQuietTimer = setTimeout(() => { store(TRAY_KEY, State.tray); updateDock(); }, 250); }
function paintFootQuiet() { const f = $('.grid-foot'); if (f) f.replaceWith(gridFoot()); }
const paintLeafQuiet = debounce(() => paintStage({ leafOnly: true }), 120);

/* What the print path would say about this row: shown in its Check cell. */
async function checkRow(d, i) {
  const st = rowState(d);
  const cell = () => $(`.grid-row[data-i="${i}"] .cell-check`);
  const show = (cls, text, title) => { const c = cell(); if (c) clear(c, h('span.check.' + cls, { title: title || text }, text)); };
  d._ok = false;
  if (st.problem) { show('bad', st.problem); return; }
  if (!d.amount) { show('muted', 'needs amount'); return; }
  if (!d.cheque_date) { show('bad', 'needs date'); return; }
  const res = await composeFor(d);
  if (State.tray[i] !== d) return;
  if (!res.ok) { show('bad', 'amount?', res.error); return; }
  d.amount_paise = res.amount_paise;
  const rep = fitReport(d, res);
  d._ok = !rep.issues.length;
  const usual = st.entity ? usualPaise(st.entity) : 0;
  const pct = usual ? Math.round((res.amount_paise - usual) / usual * 100) : 0;
  if (rep.issues.length) show('bad', 'won’t fit', rep.issues.join(' '));
  else if (rep.wordsSize) show('warn', `words shrink ${rep.wordsSize}`);
  else if (rep.payeeSize) show('warn', `payee shrinks ${rep.payeeSize}`);
  else if (usual && Math.abs(pct) >= 10) show('warn', `${pct > 0 ? '+' : ''}${pct}% vs usual`, `usual ${rupees(usual, { always: true })}`);
  else if (rep.notes.length) show('warn', 'see note', rep.notes.join(' '));
  else if (st.fresh) show('warn', 'new payee', 'No entity prints this name yet; one is created when the cheque is recorded.');
  else if (st.oneOff) show('ok', 'fits · one-off', 'Recorded as a one-off: kept out of the rhythm.');
  else show('ok', 'fits');
  paintFootQuiet();
}

function addRow() {
  State.tray.push(blankRow());
  saveTray();
  focusNewRow('.cell-entity');
}

/* A cheque to someone paid once: no entity to pick, the payee is typed. */
function addOneOff() {
  State.tray.push(blankRow({ one_off: true }));
  saveTray();
  focusNewRow('.cell-entity');
}

function focusNewRow(sel) {
  TrayPage.cur = State.tray.length - 1;
  route();
  const rows = $$('.grid-row[data-i]');
  if (rows.length) $(sel, rows[rows.length - 1]).focus();
}

function fillFromEntity(i, e) {
  const d = State.tray[i];
  const typedAmount = d.amount;
  Object.assign(d, draftFor(e), typedAmount ? { amount: typedAmount, amount_paise: paiseOf(typedAmount) || 0 } : {});
  delete d._typed;
  saveTray();
}

/* Write a cheque from anywhere — the Desk, ⌘K — by putting it in the tray and
   opening the tray on its row. A fixed-name entity already in the tray is
   reused; a payee-per-cheque entity gets a row per cheque. */
function writeInTray({ entity, amount, payee, oneOff } = {}) {
  let k = entity && !isPerCheque(entity) ? State.tray.findIndex(d => d.entity_id === entity.id) : -1;
  if (k < 0) {
    State.tray.push(entity ? draftFor(entity) : blankRow({ one_off: !!oneOff, payee: payee || '' }));
    k = State.tray.length - 1;
  }
  if (amount) Object.assign(State.tray[k], { amount, amount_paise: paiseOf(amount) || 0 });
  saveTray();
  TrayPage.cur = k;
  TrayPage.creating = null;
  go('tray');
  const row = $$('.grid-row[data-i]')[k];
  if (!row) return;
  const target = entity && isPerCheque(entity) ? $('.cell-payee', row) : entity ? $('.cell-amt', row) : $('.cell-entity', row);
  if (target) { target.focus(); target.select && target.select(); }
}

/* ------------------------------------------------------------- picker */
let picker = null;

function openPicker(input, i) {
  closePicker(true);
  const q = fold(input.value);
  const d = State.tray[i];
  const current = rowState(d).entity;
  const showAll = current && input.value === entityLabel(current);
  const list = State.entities.filter(e => showAll || !q || fold([e.entity_name, e.payee_name, e.category].join(' ')).includes(q))
    .sort((a, b) => entityLabel(a).localeCompare(entityLabel(b))).slice(0, 8);
  const items = list.map(e => ({ e }));
  const typed = input.value.trim();
  if (q && !showAll && !State.entities.some(e => fold(entityLabel(e)) === q)) items.push({ create: typed });
  if (q && !showAll) items.push({ oneOff: typed });
  const kindTag = e => e.kind === 'one_off' ? h('span.tag', 'one-off') : isPerCheque(e) ? h('span.tag', 'payee per cheque')
    : rhythmOf(e.id) ? h('span.tag', rhythmOf(e.id).cadence) : null;
  const pop = h('div.picker', { role: 'listbox' },
    h('div.picker-head', q && !showAll ? `Entities matching “${typed}”` : 'Entities'),
    items.map((it, k) => it.create != null
      ? h('div.picker-item.picker-create', { role: 'option', dataset: { k: String(k) } },
          icon('plus'), h('div', h('div.strong', `Create entity “${it.create}…”`),
            h('div.sub', 'Name it, set the payee printed on the leaf, head and date rule')), h('kbd', 'Enter'))
      : it.oneOff != null
      ? h('div.picker-item.picker-oneoff', { role: 'option', dataset: { k: String(k) } },
          icon('pen'), h('div', h('div.strong', `Use “${it.oneOff}” as a one-off payee`),
            h('div.sub', 'No entity — prints this name, kept out of the rhythm')), h('kbd', '⇧ Enter'))
      : h('div.picker-item', { role: 'option', dataset: { k: String(k) } },
          h('div', h('div.strong', entityLabel(it.e), kindTag(it.e)),
            h('div.sub', `${isPerCheque(it.e) ? 'payee typed per cheque' : 'pays ' + it.e.payee_name}${it.e.category ? ' · ' + it.e.category : ''}${it.e.last_date ? ' · last ' + fmtShort(it.e.last_date) : ''}`)),
          h('span.mono', usualPaise(it.e) ? rupees(usualPaise(it.e), { whole: true }) : '—'))),
    h('div.picker-foot', '↑↓ choose · Enter use · ⇧Enter one-off · esc close'));
  const oneOffAt = items.findIndex(it => it.oneOff != null);
  const createAt = items.findIndex(it => it.create != null);
  picker = { pop, items, i, input,
             k: d.one_off && oneOffAt >= 0 ? oneOffAt : !list.length && createAt >= 0 ? createAt : 0 };
  $$('.picker-item', pop).forEach(n => n.addEventListener('mousedown', ev => { ev.preventDefault(); pickerUse(Number(n.dataset.k)); }));
  const wrap = input.closest('.tray-grid-wrap');
  const r = input.getBoundingClientRect(), w = wrap.getBoundingClientRect();
  Object.assign(pop.style, { left: (r.left - w.left) + 'px', top: (r.bottom - w.top + wrap.scrollTop + 4) + 'px' });
  wrap.appendChild(pop);
  hydrateIcons(pop);
  paintPicker();
}

function paintPicker() {
  if (!picker) return;
  $$('.picker-item', picker.pop).forEach(n => n.classList.toggle('is-active', Number(n.dataset.k) === picker.k));
}

function closePicker(now) {
  if (picker && (now || !picker.pop.contains(document.activeElement))) { picker.pop.remove(); picker = null; }
}

function pickerKeys(ev, i) {
  if (!picker) { if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') moveKeys(ev, i); return; }
  if (ev.key === 'ArrowDown') { ev.preventDefault(); picker.k = Math.min(picker.items.length - 1, picker.k + 1); paintPicker(); }
  else if (ev.key === 'ArrowUp') { ev.preventDefault(); picker.k = Math.max(0, picker.k - 1); paintPicker(); }
  else if (ev.key === 'Enter') {
    ev.preventDefault();
    const one = picker.items.findIndex(it => it.oneOff != null);
    pickerUse(ev.shiftKey && one >= 0 ? one : picker.k);
  }
  else if (ev.key === 'Escape') { ev.preventDefault(); closePicker(true); }
}

function pickerUse(k) {
  if (!picker) return;
  const it = picker.items[k];
  const i = picker.i;
  closePicker(true);
  if (!it) return;
  if (it.create != null) { openCreate(i, it.create); return; }
  const d = State.tray[i];
  if (it.oneOff != null) {
    Object.assign(d, { payee: it.oneOff.split(/\s+/).filter(Boolean).join(' '), entity_id: null, one_off: true });
    delete d._typed;
    saveTray();
    route();
    const row = $$('.grid-row[data-i]')[i];
    if (row) $('.cell-amt', row).focus();
    return;
  }
  fillFromEntity(i, it.e);
  route();
  const row = $$('.grid-row[data-i]')[i];
  if (!row) return;
  const next = isPerCheque(it.e) ? $('.cell-payee', row) : $('.cell-amt', row);
  next.focus();
  if (!isPerCheque(it.e)) next.select();
  else next.setSelectionRange(next.value.length, next.value.length);
}

/* ------------------------------------------------------------- stage */
/* The leaf previews the row you are on. Nothing is typed on it: every field
   comes from the row, through ChequeLayout.render and the live calibration,
   exactly as the print path will lay it down. */
/* Construction view on the Tray's leaf: a viewing aid only, remembered per
   browser. It changes nothing about the row or what prints. */
function constructToggle(view) {
  const on = !window.matchMedia('(max-width: 760px)').matches && stored('desk.construct', true);
  const box = h('input', { type: 'checkbox', checked: on });
  box.addEventListener('change', () => {
    store('desk.construct', box.checked);
    box.closest('.toggle').classList.toggle('is-on', box.checked);
    view.setConstruct(box.checked);
  });
  return h('label.toggle' + (on ? '.is-on' : ''), box, h('span', 'Construction'));
}

/* The leaf, its heading and its toggles are built once per page and then only
   updated: rebuilding them for every row made the rows underneath jump. */
function buildStage(leafBox) {
  const P = TrayPage;
  const view = makeLeaf({ construct: true });
  const boxes = {};
  const toggle = (key, label) => {
    const box = boxes[key] = h('input', { type: 'checkbox' });
    box.addEventListener('change', () => {
      const d = State.tray[P.cur];
      if (!d) return;
      d[key] = box.checked;
      box.closest('.toggle').classList.toggle('is-on', box.checked);
      saveTrayQuiet();
      if (key === 'crossing') { const c = $(`.grid-row[data-i="${P.cur}"] .cell-ac input`); if (c) c.checked = box.checked; }
      checkRow(d, P.cur);
      paintStage({ leafOnly: true });
    });
    return h('label.toggle', box, h('span', label));
  };
  const title = h('span.eyebrow');
  clear(leafBox,
    h('div.stage-head',
      title,
      h('span.sub', { title: 'Read-only: type in the row below' }, '↑ ↓ change row'),
      h('span.sub.mono', `cal v${State.cal ? State.cal.version : '—'}`),
      // The row's own actions sit with its heading; the toolbar under the
      // leaf is only for what is drawn on it.
      h('div.tool-acts',
        h('button.tool-btn', { type: 'button', onclick: () => testRow(P.cur) }, icon('printer'), 'Test on plain paper'),
        h('button.tool-btn', { type: 'button', onclick: () => logRow(P.cur) }, icon('pen'), 'Record as handwritten'))),
    h('div.mat', view.el),
    h('div.leaf-tools',
      toggle('crossing', 'A/C payee crossing'), toggle('strike_order', 'Strike OR ORDER'), toggle('star_fill', 'Star-fill'),
      constructToggle(view)),
    h('div.fit-line'));
  $('.cx-slot')?.replaceChildren(view.legend);
  P.view = view;
  P.stageUi = { title, boxes };
  // On a phone the leaf is too small for the measurements to be read.
  view.setConstruct(!window.matchMedia('(max-width: 760px)').matches && stored('desk.construct', true));
  hydrateIcons(leafBox);
  // Sized now rather than on the observer's first callback: a redraw that
  // lays out at the wrong height for a frame moves the rows under it.
  view.fit();
}

function paintStage(opts = {}) {
  const P = TrayPage;
  const leafBox = $('.tray-leaf'), mem = $('.tray-memory');
  if (!leafBox) return;
  const d = State.tray[P.cur];
  if (!d) { leafBox.replaceChildren(); if (mem && !P.creating) mem.replaceChildren(); if (P.creating && mem) paintCreate(mem); return; }
  const st = rowState(d);
  const e = st.entity;
  const fresh = !leafBox.firstChild;
  if (fresh) buildStage(leafBox);
  if (fresh || !opts.leafOnly) {
    P.stageUi.title.textContent = `Preview · row ${P.cur + 1} of ${State.tray.length}${e ? ' · ' + entityLabel(e) : st.oneOff ? ' · one-off' : ''}`;
    for (const [key, box] of Object.entries(P.stageUi.boxes)) {
      box.checked = key === 'crossing' ? d.crossing !== false : !!d[key];
      box.closest('.toggle').classList.toggle('is-on', box.checked);
    }
    if (!opts.leafOnly && mem) { if (P.creating) paintCreate(mem); else paintMemory(mem, d, st); }
  }
  const view = P.view;
  view.draw(leafPayload(d, null));
  composeFor(d).then(res => {
    if (State.tray[P.cur] !== d || P.view !== view) return;
    view.draw(leafPayload(d, res && res.ok ? res : null));
    paintFitLine(d, res);
  });
}

function paintFitLine(d, res) {
  const line = $('.tray-leaf .fit-line');
  if (!line) return;
  const st = rowState(d);
  const items = [];
  if (st.problem) items.push(['bad', { 'needs payee': 'Type who this cheque is for.', 'needs entity': 'Pick an entity, or write it as a one-off.',
                                       'pick which entity': 'Several entities print this name — pick which one this cheque is for.' }[st.problem]]);
  if (res && res.ok) {
    const rep = fitReport(d, res);
    rep.issues.forEach(t => items.push(['bad', t]));
    rep.notes.forEach(t => items.push(['warn', t]));
    if (rep.wordsSize) items.push(['warn', `The words shrink to ${rep.wordsSize}mm to fit the two lines.`]);
    if (rep.payeeSize) items.push(['warn', `The payee shrinks to ${rep.payeeSize}mm to fit the Pay line.`]);
  } else if (d.amount) items.push(['bad', res ? res.error : 'That amount is not a number of rupees.']);
  else items.push(['muted', 'Type the amount in the row.']);
  if (st.entity && st.entity.seal_required) items.push(['warn', `${entityLabel(st.entity)} needs the seal after signing.`]);
  if (d.cheque_date > todayISO()) items.push(['pdc', `Post-dated to ${fmtDay(d.cheque_date)}. The bank will not honour it before then.`]);
  const allGood = res && res.ok && !items.some(([t]) => t === 'bad' || t === 'warn');
  clear(line,
    allGood ? h('span.fit.ok', icon('check'), 'All fields fit · nothing in the MICR band') : null,
    items.map(([t, text]) => h('span.fit.' + t, t === 'bad' ? icon('alert') : t === 'pdc' ? icon('calendar') : null, text)));
  hydrateIcons(line);
}

/* Beside the leaf: what the register knows about the row's entity. */
const historyCache = new Map();
function paintMemory(mem, d, st) {
  const e = st.entity;
  if (!e) {
    clear(mem,
      h('div.eyebrow', st.oneOff ? 'One-off' : 'Entity'),
      st.oneOff
        ? h('div', h('h3', d.payee), h('p.sub', 'No entity. The cheque is recorded as a one-off: kept out of the rhythm and grouped under One-off in reports.'))
        : st.fresh
          ? h('div', h('h3', d.payee), h('p.sub', 'No entity prints this name yet. Recording the cheque creates one, named as printed.'))
          : h('p.sub', 'Pick an entity in the row: its printed name, usual amount, date rule and crossing fill in.'));
    return;
  }
  const r = rhythmOf(e.id);
  const draw = hist => {
    if (TrayPage.cur < 0 || State.tray[TrayPage.cur] !== d) return;
    const amountP = d.amount_paise || paiseOf(d.amount) || 0;
    const lines = [];
    if (hist && hist.median_paise && amountP) {
      const pct = Math.round((amountP - hist.median_paise) / hist.median_paise * 100);
      lines.push(Math.abs(pct) <= 2 ? `${rupees(amountP, { whole: true })} is within 2% of usual.`
        : `${rupees(amountP, { whole: true })} is ${Math.abs(pct)}% ${pct > 0 ? 'more' : 'less'} than usual.`);
    }
    if (r && r.next_due && e.kind !== 'one_off') {
      const dd = r.days_until_due;
      lines.push(`Due ${fmtShort(r.next_due)}${dd < 0 ? `, so ${plural(-dd, 'day')} late` : dd === 0 ? ', today' : `, in ${plural(dd, 'day')}`}.`);
    }
    if (e.date_rule && e.date_rule !== 'today') lines.push(`Cheques are ${ruleText(e.date_rule)}.`);
    clear(mem,
      h('div.eyebrow', `Row ${TrayPage.cur + 1} · entity`),
      h('h3', entityLabel(e)),
      h('p.sub', [isPerCheque(e) ? 'payee typed per cheque' : e.entity_name && e.entity_name !== e.payee_name ? `prints as ${e.payee_name}` : '',
                  e.category, e.kind === 'one_off' ? 'one-off' : r ? r.cadence : '',
                  e.ac_payee ? 'A/C payee by default' : 'not crossed by default'].filter(Boolean).join(' · ')),
      hist && hist.cheques.length ? historyBars(hist, amountP) : h('p.sub', e.count ? 'Loading its last cheques…' : 'No cheques yet.'),
      lines.length ? h('div.mem-callout', lines.join(' ')) : null,
      hist && hist.cheques.length ? h('div.mem-block', h('div.eyebrow', 'Recent cheques'),
        h('div.recent', hist.cheques.slice().reverse().slice(0, 4).map(c => h('div.recent-row',
          h('span.mono', c.cheque_no || '—'), h('span', isPerCheque(e) ? c.payee : fmtShort(c.cheque_date)),
          h('span.mono', rupees(c.amount_paise, { whole: true })),
          h('span.tag.st-' + c.status, { printed: 'printed', logged: 'handwritten', queued: 'at printer' }[c.status] || c.status))))) : null,
      h('div.mem-foot', h('button.btn-link', { type: 'button', onclick: async () => { if (await editEntity(e)) route(); } }, 'Edit entity')));
  };
  if (historyCache.has(e.id)) { draw(historyCache.get(e.id)); return; }
  draw(null);
  if (!e.count) return;
  api(`/api/entities/${e.id}/history?limit=8`).then(res => { historyCache.set(e.id, res); draw(res); }).catch(() => {});
}
on('desk', () => historyCache.clear());

/* The last cheques as bars on one scale, with a line at their median and the
   cheque being written as the last bar. */
function historyBars(hist, amountP) {
  const vals = hist.cheques.map(c => c.amount_paise);
  const all = amountP ? vals.concat([amountP]) : vals;
  const max = Math.max(1, ...all);
  const bars = h('div.bars');
  hist.cheques.forEach(c => bars.appendChild(h('div.bar', { title: `${fmtDay(c.cheque_date)} · ${rupees(c.amount_paise, { always: true })}` },
    h('i', { style: { height: (c.amount_paise / max * 100) + '%' } }), h('span', MONTHS[parseISO(c.cheque_date).getMonth()]))));
  if (amountP) bars.appendChild(h('div.bar.is-now', { title: `This cheque · ${rupees(amountP, { always: true })}` },
    h('i', { style: { height: (amountP / max * 100) + '%' } }), h('span', 'now')));
  if (hist.median_paise) bars.appendChild(h('div.median', { style: { bottom: `calc(${hist.median_paise / max * 100}% * 0.82 + 18px)` } }));
  return h('div.mem-block',
    h('div.mem-row', h('span.eyebrow', `Last ${plural(hist.cheques.length, 'cheque')}`), h('span.sub', `median ${rupees(hist.median_paise, { whole: true })}`)),
    bars);
}

function remindText(days) {
  const ds = String(days || '').split(',').filter(Boolean).map(Number).sort((a, b) => b - a);
  if (!ds.length) return 'never';
  return ds.map(x => x === 0 ? 'on the day' : `${x} day${x === 1 ? '' : 's'} before`).join(', ');
}

function incrementNo(no) {
  const m = /^(.*?)(\d+)$/.exec(String(no || '').trim());
  if (!m) return '';
  return m[1] + String(Number(m[2]) + 1).padStart(m[2].length, '0');
}

function leafAt(first, k) {
  let n = first;
  for (let j = 0; j < k; j++) n = incrementNo(n);
  return n;
}

/* ------------------------------------------------------------- one row, off the printer */
async function rowReady(i) {
  const d = State.tray[i];
  if (!d) return null;
  const st = rowState(d);
  if (st.problem) { toast(`Row ${i + 1}: ${st.problem}.`, true); return null; }
  const res = await composeFor(d);
  if (!res.ok) { toast(res.error, true); return null; }
  return { d, res };
}

async function testRow(i) {
  const ok = await rowReady(i);
  if (!ok) return;
  const choice = await modal({
    title: `Test row ${i + 1} on plain paper`,
    body: h('div',
      h('p', 'The cheque is rendered and sent exactly as it would be for a real leaf, with the same printer settings — but no leaf number is used and nothing goes in the register.'),
      loadingNote('Load a sheet of plain paper cut to leaf size (204 × 92 mm), the same way as a leaf: face up, sideways, Pay end first. Then hold it over a real leaf against the light.')),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Print the test', primary: true, value: 'go' }],
  });
  if (choice !== 'go') return;
  try {
    await post('/api/test-print/cheque', Object.assign(issueBody(ok.d), { confirm: true, printer: State.printer }));
    toast('Test sent to the printer. Hold it over a real leaf before printing.');
  } catch (err) { toast(err.message, true); }
}

/* A leaf filled in by pen: the register records it, nothing is printed. */
async function logRow(i) {
  const ok = await rowReady(i);
  if (!ok) return;
  const leaf = h('input.mono', { type: 'text', value: TrayPage.firstNo || nextLeaf(), 'aria-label': 'Leaf number' });
  const choice = await modal({
    eyebrow: `Row ${i + 1}`, title: 'Record a cheque written by hand',
    body: h('div', chequeFacts(ok.d, ok.res),
      h('p', 'Nothing is sent to the printer. Copy the words above onto the leaf exactly as they read, then record it.'),
      field('Leaf number', leaf)),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Record it', primary: true, value: 'log' }],
  });
  if (choice !== 'log') return;
  const no = leaf.value.trim();
  const usedMsg = await leafNumberFree(no);
  if (usedMsg) { toast(usedMsg, true); return; }
  try {
    const res = await post('/api/cheques', Object.assign(issueBody(Object.assign({}, ok.d, { cheque_no: no })), { action: 'log' }));
    toast(res.message || 'Recorded.');
    const k = State.tray.indexOf(ok.d);
    if (k >= 0) { State.tray.splice(k, 1); saveTray(); }
    TrayPage.firstNo = '';
    await loadDesk();
    route();
  } catch (err) { toast(err.message, true); }
}

function chequeFacts(d, composed, extra = []) {
  const e = entityById(d.entity_id);
  return h('div',
    dl([
      ['Pay to', d.payee],
      e && e.entity_name && e.entity_name !== d.payee ? ['Entity', e.entity_name] : null,
      ['Amount', rupees(composed.amount_paise, { always: true }), 'big'],
      ['Date', fmtDay(d.cheque_date) + (d.cheque_date > todayISO() ? ' · post-dated' : '')],
      ['Crossing', d.crossing ? 'A/C payee only' : 'none'],
      ...extra,
    ]),
    h('div.words-box', composed.amount_words));
}

async function leafNumberFree(no) {
  if (!no) return '';
  try {
    const res = await api(`/api/leaves/${encodeURIComponent(no)}`);
    if (res.used) return `Leaf ${no} is already in the register — a used leaf number is never written again.`;
  } catch (err) { return err.message; }
  return '';
}

function issueBody(d) {
  return { payee: d.payee.trim(), entity_id: d.entity_id, one_off: d.entity_id == null && !!d.one_off,
           amount: d.amount, cheque_date: d.cheque_date, cheque_no: d.cheque_no || '', memo: d.memo || '',
           crossing: d.crossing, strike_order: d.strike_order, star_fill: d.star_fill };
}

/* ------------------------------------------------------------- create */
function openCreate(i, typed) {
  if (i == null) { State.tray.push(blankRow()); saveTray(); i = State.tray.length - 1; }
  TrayPage.creating = { index: i, typed };
  TrayPage.cur = i;
  route();
}

function paintCreate(side) {
  const P = TrayPage;
  const { index, typed } = P.creating;
  const d = State.tray[index];
  const form = entityForm(null, { entity_name: typed, amount: d ? d.amount : '' }, { withAmount: true });
  const save = h('button.btn.btn-primary', { type: 'button' }, `Create and fill row ${index + 1}`);
  save.addEventListener('click', async () => {
    try {
      const values = form.values();
      if (!values.entity_name && values.payee_name) values.entity_name = values.payee_name;
      const made = await saveEntity(null, values);
      P.creating = null;
      fillFromEntity(index, made);
      const a = form.amount();
      if (a) { State.tray[index].amount = a; State.tray[index].amount_paise = paiseOf(a) || 0; saveTray(); }
      toast(`${entityLabel(made)} created and row ${index + 1} filled.`);
      route();
    } catch (err) { form.showError(err.message); }
  });
  clear(side,
    h('div.side-head', h('span.eyebrow', `New entity · for row ${index + 1}`)),
    form.el,
    h('div.btn-row', save, h('button.btn.btn-quiet', { type: 'button', onclick: () => { P.creating = null; route(); } }, 'Cancel')),
    h('p.foot', 'Saved through the existing POST /api/entities · no print code involved'));
  hydrateIcons(side);
  setTimeout(() => form.focus(), 30);
}

/* ------------------------------------------------------------- paste */
async function pasteRows() {
  const area = h('textarea.paste', { rows: 10, placeholder: 'City Power Board\t67865\t16/10/2026\telectricity — Oct\nShield Security\t96,991' });
  const choice = await modal({
    title: 'Paste cheques from a sheet', wide: true,
    body: h('div', h('p', 'One cheque per line: entity or payee, amount, and optionally the date and a note. Columns copied from a spreadsheet are separated by tabs; a line without tabs is split at semicolons. Names are matched to entities; anything unknown comes in as a row to create.'), area),
    onOpen: () => area.focus(),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Add the rows', primary: true, value: 'add' }],
  });
  if (choice !== 'add') return;
  let added = 0;
  area.value.split(/\r?\n/).map(l => l.trim()).filter(Boolean).forEach(line => {
    const cols = (line.includes('\t') ? line.split('\t') : line.split(';')).map(c => c.trim());
    const [who, amt, dt, note] = cols;
    if (!who) return;
    const q = fold(who);
    const matches = State.entities.filter(e => fold(entityLabel(e)) === q || fold(e.payee_name) === q);
    let date = todayISO();
    const m1 = /^(\d{1,2})[/.-](\d{1,2})[/.-](\d{4})$/.exec(dt || ''), m2 = /^\d{4}-\d{2}-\d{2}$/.exec(dt || '');
    if (m1) date = `${m1[3]}-${m1[2].padStart(2, '0')}-${m1[1].padStart(2, '0')}`;
    else if (m2) date = dt;
    const amount = String(amt || '').replace(/[₹,\s]/g, '');
    const row = matches.length === 1
      ? draftFor(matches[0], Object.assign({ cheque_date: dt ? date : matches[0].rule_date || todayISO() }, amount ? { amount, amount_paise: paiseOf(amount) || 0 } : {}))
      : { payee: '', _typed: who, entity_id: null, amount, amount_paise: paiseOf(amount) || 0, cheque_date: date, memo: '',
          crossing: true, strike_order: false, star_fill: false, one_off: false };
    if (note) row.memo = note;
    State.tray.push(row);
    added++;
  });
  saveTray();
  toast(`${plural(added, 'row')} added.`);
  route();
}

/* ------------------------------------------------------------- footer */
function gridFoot() {
  const rows = State.tray;
  const total = rows.reduce((s, d) => s + (d.amount_paise || 0), 0);
  const pdc = rows.filter(d => d.cheque_date > todayISO()).length;
  const books = State.desk.books;
  const inUse = books.books.find(b => b.id === books.in_use);
  const first = TrayPage.firstNo || nextLeaf();
  const fits = inUse ? Math.min(rows.length, inUse.left) : rows.length;
  const glance = rows.filter(d => !d._ok).length;
  const firstInput = h('input.mono.first-no', { type: 'text', value: first, inputmode: 'numeric', 'aria-label': 'First leaf number', placeholder: 'first leaf' });
  firstInput.addEventListener('input', () => { TrayPage.firstNo = firstInput.value.trim(); });
  let leavesText;
  if (!rows.length) leavesText = '';
  else if (inUse) {
    leavesText = `Needs ${plural(rows.length, 'leaf', 'leaves')}: ${rows.length > 1 ? `${first}–${leafAt(first, rows.length - 1)}` : first} from ${inUse.name} (${inUse.left} left).`;
    if (rows.length > inUse.left) leavesText = `${inUse.name} has ${inUse.left} leaves left, so this run takes the first ${fits} rows and stops at the end of the book; the rest stay in the tray for the next book.`;
  } else leavesText = first ? `Needs ${plural(rows.length, 'leaf', 'leaves')}: ${rows.length > 1 ? `${first}–${leafAt(first, rows.length - 1)}` : first}.` : 'Type the number of the first leaf you will load.';
  const one = rows.length === 1;
  const blocked = healthBlocksPrint();
  const allOk = rows.length && rows.slice(0, fits).every(d => d._ok);
  return h('div.grid-foot',
    h('div',
      h('div.strong', rows.length ? `${plural(rows.length, 'cheque')} · ${rupees(total, { always: true })}${pdc ? ` · ${pdc} post-dated` : ''}` : 'No cheques'),
      h('div.sub', leavesText, glance && rows.length ? ` ${plural(glance, 'row needs', 'rows need')} a glance.` : '')),
    h('label.first-wrap', h('span', 'First leaf'), firstInput),
    h('div.btn-row',
      h('button.btn.btn-quiet', { type: 'button', onclick: () => { store(TRAY_KEY, State.tray); State.dirty = false; go('desk'); } }, 'Keep in tray'),
      one
        ? h('button.btn.btn-primary', { type: 'button', disabled: !allOk || !!blocked || TrayPage.busy, title: blocked, onclick: () => printSingle(0) }, icon('printer'), 'Print this cheque…')
        : h('button.btn.btn-quiet', { type: 'button', disabled: !allOk || !!blocked || TrayPage.busy || rows.length < 2, title: blocked, onclick: () => startRun('test') }, 'Test on plain paper first'),
      one ? null : h('button.btn.btn-primary', { type: 'button', disabled: !allOk || !!blocked || TrayPage.busy || rows.length < 2, title: blocked, onclick: () => startRun('first') },
        icon('printer'), 'Print the first leaf…')));
}

/* ------------------------------------------------------------- single */
async function printSingle(i) {
  const d = State.tray[i];
  const no = TrayPage.firstNo || nextLeaf();
  const res = await composeFor(d);
  if (!res.ok) return;
  const usedMsg = await leafNumberFree(no);
  if (usedMsg) { toast(usedMsg, true); return; }
  const choice = await modal({
    eyebrow: no ? `Leaf ${no}` : 'Unnumbered leaf', title: 'Print onto a cheque leaf',
    body: h('div', chequeFacts(d, res, [['Leaf', no || 'no number recorded']]),
      h('p.sub', 'A single cheque is not a batch: it gets one verdict, Printed or Misprinted.'),
      loadingNote(`Load leaf ${no} face up, sideways, with the Pay end going in first.`)),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Print it', primary: true, value: 'print' }],
  });
  if (choice !== 'print') return;
  TrayPage.busy = true;
  try {
    const out = await post('/api/cheques', Object.assign(issueBody(Object.assign({}, d, { cheque_no: no })),
      { action: 'print', confirm: true, printer: State.printer }));
    TrayPage.single = { id: out.id, cheque_no: no, draft: d };
  } catch (err) { toast(err.message, true); }
  TrayPage.busy = false;
  await loadDesk();
  route();
}

function renderSingle(root) {
  const s = TrayPage.single;
  const view = makeLeaf();
  composeFor(s.draft).then(res => view.draw(leafPayload(s.draft, res.ok ? res : null)));
  const verdict = async outcome => {
    try { await post(`/api/cheques/${s.id}/confirm`, { outcome }); } catch (err) { toast(err.message, true); return; }
    const k = State.tray.indexOf(s.draft);
    if (outcome === 'printed' && k >= 0) { State.tray.splice(k, 1); saveTray(); }
    toast(outcome === 'printed' ? `Leaf ${s.cheque_no} recorded as printed.` : `Leaf ${s.cheque_no} recorded as misprinted; the cheque stays in the tray for the next leaf.`);
    TrayPage.single = null;
    TrayPage.firstNo = '';
    await loadDesk();
    route();
  };
  appendTo(root, 
    h('div.page-head', h('div',
      h('div.eyebrow', `Leaf ${s.cheque_no || '—'} · at the printer`),
      h('h2.headline', 'Did it print correctly?'),
      h('p.lede', 'One cheque, one verdict. Nothing is recorded as printed until you say so.'))),
    h('div.run-body', h('div.mat', view.el),
      h('div.verdict-bar',
        h('div', h('div.strong', `${s.draft.payee} · ${rupees(s.draft.amount_paise, { always: true })}`), h('div.sub', fmtDay(s.draft.cheque_date))),
        h('div.btn-row',
          h('button.btn.btn-danger-quiet', { type: 'button', onclick: () => verdict('failed') }, icon('x'), 'Misprinted'),
          h('button.btn.btn-ok', { type: 'button', onclick: () => verdict('printed') }, icon('check'), 'Printed correctly')))));
}

/* ------------------------------------------------------------- start a run */
async function startRun(stage) {
  const books = State.desk.books;
  const inUse = books.books.find(b => b.id === books.in_use);
  const rows = State.tray;
  const take = inUse ? Math.min(rows.length, inUse.left) : rows.length;
  const first = TrayPage.firstNo || nextLeaf();
  if (!first) { toast('Type the number of the first leaf you will load.', true); $('.first-no') && $('.first-no').focus(); return; }
  if (take < 2) {
    toast(take ? 'Only one leaf is left in this book — print that cheque on its own, then load the next book.' : 'The book in use has no leaves left. Add or switch books in the Register.', true);
    return;
  }
  const items = rows.slice(0, take).map(({ amount_paise, _ok, _typed, ...rest }) => rest);
  TrayPage.busy = true;
  try {
    const b = await post('/api/batches', { items, first_cheque_no: first, printer: State.printer });
    State.tray.splice(0, take);
    saveTray();
    setRunStage(b, stage);
    TrayPage.ticks = new Set();
    TrayPage.misprinted = new Set();
    TrayPage.finished = null;
    TrayPage.firstNo = '';
    State.dirty = false;
    if (take < rows.length) toast(`${plural(rows.length - take, 'cheque')} stay in the tray for the next book.`);
  } catch (err) { toast(err.message, true); }
  TrayPage.busy = false;
  await loadDesk();
  route();
}

/* ------------------------------------------------------------- run */
const STEPS = [['write', 'Write'], ['first', 'First leaf alone'], ['first_sent', 'First verdict'],
               ['first_ok', 'Rest sent'], ['rest_sent', 'Batch verdict']];

function stepper(stage) {
  // The plain-paper test is a detour, shown as a step only while it is taken.
  const steps = stage === 'test' ? [STEPS[0], ['test', 'Plain-paper test'], ...STEPS.slice(1)] : STEPS;
  const at = steps.findIndex(([k]) => k === stage);
  return h('ol.stepper', steps.map(([k, label], n) => h('li' + (n < at ? '.is-done' : n === at ? '.is-current' : ''),
    n === at ? { 'aria-current': 'step' } : null, h('span.step-n', n < at ? icon('check') : String(n + 1)), h('span', label))));
}

async function batchStep(b, path, body, after) {
  if (TrayPage.busy) return;
  TrayPage.busy = true;
  route();
  try {
    const res = await post(`/api/batches/${b.id}/${path}`, body || {});
    if (after) await after(res);
  } catch (err) {
    toast(err.message, true);
  }
  TrayPage.busy = false;
  await loadDesk();
  route();
}

async function returnLeftovers(leftovers) {
  const back = [];
  for (const it of leftovers) {
    const res = await composeFor(it);
    back.push(Object.assign({}, it, { amount_paise: res.ok ? res.amount_paise : 0 }));
  }
  State.tray.unshift(...back);
  saveTray();
  return back.length;
}

function renderRun(root, b) {
  const stage = runStage(b);
  const drafts = b.items.filter(i => i.status === 'draft');
  const head = {
    test: ['Plain paper · same render, same printer settings', 'Hold the test sheet over a real leaf, against the light.',
           'The printer puts the first cheque on plain paper cut to leaf size, through exactly the same render and printer settings a real leaf gets. If the ink sits in the boxes here, it will on the leaf. No leaf number is used and nothing is recorded as issued.'],
    first: ['First leaf alone', `Load one leaf — No. ${drafts[0] ? drafts[0].cheque_no : ''}.`,
            'The first cheque prints on its own so a bad feed or a calibration slip costs one leaf, not the stack.'],
    first_sent: ['First verdict', `Check leaf ${(b.items.find(i => i.status === 'queued') || {}).cheque_no || ''}.`, 'Did it print correctly, in the right place? Only this leaf is checked on its own.'],
    first_ok: ['First leaf confirmed', `Load ${plural(b.to_print, 'leaf', 'leaves')} — Nos. ${b.first_leaf}${b.to_print > 1 ? ' to ' + b.last_leaf : ''}.`,
               `Stack them so No. ${b.first_leaf} feeds first. Each leaf prints with the cheque listed against its number.`],
    rest_sent: ['First leaf confirmed', 'Confirm the rest in one go.',
                `Only the first leaf was checked on its own. The other ${countWord(b.items.filter(i => i.status === 'queued').length, true)} get one batch confirmation; mark a leaf only if it came out wrong.`],
  }[stage];
  appendTo(root, 
    h('div.page-head', h('div',
      h('div.eyebrow', `Batch #${b.id} · ${plural(b.count, 'leaf', 'leaves')} · ${rupees(b.total_paise, { always: true })} · ${head[0]}`),
      h('h2.headline', head[1]),
      h('p.lede', head[2]))),
    stepper(stage));
  const body = h('div.run-body');
  root.appendChild(body);
  if (stage === 'test') runTest(body, b);
  else if (stage === 'first') runFirst(body, b);
  else if (stage === 'first_sent') runFirstVerdict(body, b);
  else if (stage === 'first_ok') runRest(body, b);
  else if (stage === 'rest_sent') runRestVerdict(body, b);
}

function itemLeaf(it, opts) {
  const view = makeLeaf(opts);
  view.draw({ payee: it.payee, cheque_date: it.cheque_date, amount_words: it.amount_words, amount_figures: it.amount_figures,
              crossing: it.crossing !== false, strike_order: !!it.strike_order, star_fill: !!it.star_fill });
  return view;
}

function cancelRun(b, label = 'Cancel the run') {
  return h('button.btn-link', { type: 'button', disabled: TrayPage.busy, onclick: () => batchStep(b, 'cancel', null, async res => {
    const n = await returnLeftovers(res.leftovers || []);
    toast(`Run stopped. ${plural(n, 'cheque')} went back to the tray.`);
  }) }, label);
}

function runTest(body, b) {
  const P = TrayPage;
  const first = b.items.find(i => i.status === 'draft');
  const view = itemLeaf(first, { test: true });
  view.setStamp('PLAIN PAPER', 'muted');
  api(`/api/events?subject=${encodeURIComponent('batch:' + b.id)}`).then(r => {
    P.testsPrinted = r.events.filter(e => e.kind === 'test-print').length;
    const btn = $('#btnTestAgain');
    if (btn && P.testsPrinted) btn.lastChild.textContent = 'Print the test again';
  }).catch(() => {});
  const all = P.ticks.size === CHECKLIST.length;
  const list = h('div.checklist', CHECKLIST.map((text, k) => {
    const box = h('input', { type: 'checkbox', checked: P.ticks.has(k) });
    box.addEventListener('change', () => { if (box.checked) P.ticks.add(k); else P.ticks.delete(k); route(); });
    return h('label.check-item' + (P.ticks.has(k) ? '.is-on' : ''), box, h('span', text));
  }));
  const drafts = b.items.filter(i => i.status === 'draft').length;
  appendTo(body, 
    h('div.run-main', h('div.mat', view.el),
      h('p.sub', 'The test sheet is drawn here exactly over the leaf. Your eyes are the check: tick each line once you have looked.')),
    h('aside.run-side',
      h('div.eyebrow', 'What to look for'), list,
      h('div.eyebrow', 'Test which cheques'),
      seg('Test which cheques', [['first', 'First only (1 sheet)'], ['all', `All ${drafts} sheets`]], P.testWhich, v => { P.testWhich = v; }),
      h('button.btn.btn-quiet', { id: 'btnTestAgain', type: 'button', disabled: P.busy, onclick: () => testPrint(b) }, icon('printer'), h('span', 'Print the test')),
      h('button.btn.btn-primary', { type: 'button', disabled: !all || P.busy, onclick: () => { setRunStage(b, 'first'); route(); } },
        'It lines up: load the first real leaf'),
      h('p.sub', all ? 'Every line ticked.' : 'Unlocks once all six lines are ticked.'),
      h('button.btn.btn-quiet', { type: 'button', onclick: openCalibration }, 'It’s off: open calibration'),
      h('div.run-links', h('button.btn-link', { type: 'button', onclick: () => { setRunStage(b, 'first'); route(); } }, 'skip the test'), ' · ', cancelRun(b))));
}

async function testPrint(b) {
  const P = TrayPage;
  const n = P.testWhich === 'all' ? b.items.filter(i => i.status === 'draft').length : 1;
  const choice = await modal({
    title: `Print ${plural(n, 'test sheet')} on plain paper`,
    body: h('div', h('p', 'Same render, same printer settings as a real leaf; no leaf number, nothing in the register. One event is written to the batch for each sheet.'),
      loadingNote('Load plain paper cut to leaf size (204 × 92 mm), face up, sideways, Pay end first — exactly as a leaf goes in.')),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Print the test', primary: true, value: 'go' }],
  });
  if (choice !== 'go') return;
  await batchStep(b, 'test-print', { which: P.testWhich, confirm: true }, res => {
    P.testsPrinted += res.sent.length;
    toast(`${plural(res.sent.length, 'test sheet')} sent. Hold ${res.sent.length === 1 ? 'it' : 'them'} over a real leaf.`);
  });
}

function runItems(b, opts = {}) {
  return h('div.run-items', b.items.map(it => {
    const e = it.entity_id != null ? entityById(it.entity_id) : null;
    const st = { draft: 'to print', queued: 'at printer', printed: 'printed', failed: 'misprinted' }[it.status] || it.status;
    return h('div.run-item.st-' + it.status,
      h('span.mono', it.cheque_no || '—'),
      h('span.strong', e ? entityLabel(e) : (it.entity_name || it.payee)),
      h('span.sub', it.payee),
      h('span.mono.num', rupees(it.amount_paise, { always: true })),
      h('span.tag.st-' + it.status, st),
      opts.tick && it.status === 'queued' && opts.tick(it));
  }));
}

function runFirst(body, b) {
  const first = b.items.find(i => i.status === 'draft');
  const view = itemLeaf(first);
  appendTo(body, 
    h('div.run-main', h('div.mat', view.el), runItems(b)),
    h('aside.run-side',
      h('div.eyebrow', `Leaf ${first.cheque_no}`),
      h('div.strong', `${first.entity_name || first.payee} · ${rupees(first.amount_paise, { always: true })}`),
      loadingNote(`Load leaf ${first.cheque_no} face up, sideways, with the Pay end going in first. Only this one leaf.`),
      h('button.btn.btn-primary', { type: 'button', disabled: TrayPage.busy || !!healthBlocksPrint(), title: healthBlocksPrint(),
        onclick: async () => {
          const ok = await modal({ eyebrow: `Leaf ${first.cheque_no}`, title: 'Print the first cheque alone',
            body: h('div', chequeFacts(first, first, [['Leaf', first.cheque_no]]), loadingNote()),
            actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Print it', primary: true, value: 'print' }] });
          if (ok === 'print') batchStep(b, 'print-first', { confirm: true });
        } }, icon('printer'), `Print the first cheque on leaf ${first.cheque_no}`),
      h('div.run-links', h('button.btn-link', { type: 'button', onclick: () => { setRunStage(b, 'test'); route(); } }, 'test on plain paper first'), ' · ', cancelRun(b))));
}

function runFirstVerdict(body, b) {
  const sent = b.items.find(i => i.status === 'queued');
  const view = itemLeaf(sent);
  appendTo(body, 
    h('div.run-main', h('div.mat', view.el), runItems(b)),
    h('aside.run-side',
      h('div.eyebrow.tone-printer', `Leaf ${sent.cheque_no} · at the printer`),
      h('div.strong', `${sent.entity_name || sent.payee} · ${rupees(sent.amount_paise, { always: true })}`),
      h('p', 'Look at the leaf. Is every field in its place, and nothing in the MICR band?'),
      h('button.btn.btn-ok', { type: 'button', disabled: TrayPage.busy, onclick: () => batchStep(b, 'first-outcome', { outcome: 'printed' }) }, icon('check'), 'Printed correctly'),
      h('button.btn.btn-danger-quiet', { type: 'button', disabled: TrayPage.busy, onclick: () => batchStep(b, 'first-outcome', { outcome: 'failed' }, () => {
        setRunStage(b, 'first');
        toast(`Leaf ${sent.cheque_no} is spoiled; its number is never reused. Fix the cause, then the first cheque prints again on the next leaf.`);
      }) }, icon('x'), 'Misprinted'),
      h('p.sub', 'A misprinted first leaf is recorded as spoiled and the cheque moves to the next leaf; every later number moves on by one.')));
}

function runRest(body, b) {
  const books = State.desk.books;
  const inUse = books.books.find(x => x.id === books.in_use);
  const lastOk = !inUse || !b.last_leaf || leafCmp(b.last_leaf, inUse.last_no) <= 0;
  const preview = b.items.find(i => i.status === 'draft');
  const view = itemLeaf(preview);
  appendTo(body, 
    h('div.run-main', h('div.mat', view.el), runItems(b)),
    h('aside.run-side',
      h('div.eyebrow', `${plural(b.to_print, 'leaf', 'leaves')} · ${b.first_leaf}${b.to_print > 1 ? ' – ' + b.last_leaf : ''}`),
      lastOk ? loadingNote(`Stack ${plural(b.to_print, 'leaf', 'leaves')}, No. ${b.first_leaf} on top so it feeds first, face up, sideways, Pay end first.`)
        : h('div.alert-card.small', h('div.strong', `This run now crosses the end of ${inUse.name}.`),
            h('p', `${inUse.name} ends at ${inUse.last_no}, but the last cheque would go on ${b.last_leaf} — a misprint used a leaf. Stop here: the cheques that are left go back to the tray, and the next run starts on the next book with its own first leaf.`)),
      lastOk ? h('button.btn.btn-primary', { type: 'button', disabled: TrayPage.busy || !!healthBlocksPrint(), title: healthBlocksPrint(),
        onclick: async () => {
          const ok = await modal({ title: `Print ${plural(b.to_print, 'cheque')}`,
            body: h('div', runItems({ items: b.items.filter(i => i.status === 'draft') }), loadingNote(`Load ${plural(b.to_print, 'leaf', 'leaves')}, Nos. ${b.first_leaf} to ${b.last_leaf}, ${b.first_leaf} feeding first.`)),
            actions: [{ label: 'Cancel', value: 'cancel' }, { label: `Print ${b.to_print}`, primary: true, value: 'print' }] });
          if (ok === 'print') batchStep(b, 'print-rest', { confirm: true });
        } }, icon('printer'), `Print ${plural(b.to_print, 'cheque')}`) : null,
      h('div.run-links', cancelRun(b, lastOk ? 'stop here — the rest go back to the tray' : 'Stop here'))));
}

function leafCmp(a, z) {
  const pa = /^(.*?)(\d+)$/.exec(a), pz = /^(.*?)(\d+)$/.exec(z);
  if (!pa || !pz || pa[1] !== pz[1]) return 0;
  return Number(pa[2]) - Number(pz[2]);
}

function runRestVerdict(body, b) {
  const P = TrayPage;
  const waiting = b.items.filter(i => i.status === 'queued');
  const unsent = b.items.filter(i => i.status === 'draft').length;
  const bad = [...P.misprinted].filter(id => waiting.some(w => w.cheque_id === id));
  const firstPrinted = b.items.find(i => i.status === 'printed');
  const table = h('div.verdict-table', { role: 'table' },
    h('div.vt-row.vt-head', { role: 'row' }, ['Leaf', 'Entity', 'Payee printed', 'Date', 'Amount', 'Status', 'Came out wrong'].map(t => h('div', t))),
    b.items.filter(i => i.cheque_id).map(it => {
      const e = it.entity_id != null ? entityById(it.entity_id) : null;
      const tick = it.status === 'queued' ? h('input', { type: 'checkbox', checked: P.misprinted.has(it.cheque_id), 'aria-label': `Leaf ${it.cheque_no} came out wrong` }) : null;
      if (tick) tick.addEventListener('change', () => { if (tick.checked) P.misprinted.add(it.cheque_id); else P.misprinted.delete(it.cheque_id); route(); });
      return h('div.vt-row' + (P.misprinted.has(it.cheque_id) ? '.is-bad' : ''), { role: 'row' },
        h('div.mono', it.cheque_no), h('div.strong', e ? entityLabel(e) : (it.entity_name || it.payee)), h('div', it.payee),
        h('div', fmtDay(it.cheque_date)), h('div.mono.num', rupees(it.amount_paise, { always: true })),
        h('div.sub', it === firstPrinted ? 'Printed · confirmed alone' : it.status === 'queued' ? h('span', icon('layers'), ' In the batch verdict') : it.status),
        h('div', tick || '—'));
    }));
  const total = b.items.reduce((s, i) => s + i.amount_paise, 0);
  const label = bad.length ? `Record ${plural(bad.length, 'misprint')}` : `All ${waiting.length} printed correctly`;
  const confirm = () => batchStep(b, 'rest-outcome', { misprinted: bad }, async res => {
    P.misprinted = new Set();
    const n = await returnLeftovers(res.leftovers || []);
    P.finished = { batch: res.batch, back: n };
  });
  const btn = h('button.btn.btn-primary', { type: 'button', disabled: P.busy, onclick: confirm }, label, h('kbd', 'Enter'));
  appendTo(body, h('div.run-wide',
    table,
    h('div.vt-total', h('span', plural(b.count, 'leaf', 'leaves')), h('span.mono', rupees(total, { always: true }))),
    h('p.sub', 'Tick only the leaves that came out wrong. Unticked leaves are recorded as printed. A ticked leaf is spoiled (its number is never reused) and the cheque goes back to the tray for the next leaf.' +
      (unsent ? ` ${plural(unsent, 'cheque was', 'cheques were')} not sent — the printer refused a leaf — and will come back to the tray too.` : '')),
    h('div.verdict-bar',
      h('div', h('div.strong', `${plural(waiting.length, 'leaf', 'leaves')} · ${waiting[0] ? waiting[0].cheque_no : ''}${waiting.length > 1 ? ' – ' + waiting[waiting.length - 1].cheque_no : ''}`),
        h('div.sub', bad.length ? `${plural(bad.length, 'leaf', 'leaves')} marked as misprinted; the rest are recorded as printed.`
          : `Nothing ticked, so all ${countWord(waiting.length, true)} are recorded as printed: one event per cheque. Tick a leaf above and this button becomes “Record 1 misprint”.`)),
      h('div.btn-row',
        h('button.btn.btn-quiet', { type: 'button', onclick: () => toast('Take your time — nothing is recorded until you confirm.') }, 'Hold, I’ll check again'),
        btn))));
  setTimeout(() => btn.focus(), 30);
}

function finishedCard() {
  const f = TrayPage.finished;
  const b = f.batch;
  const printed = b.items.filter(i => i.status === 'printed').length;
  return h('div.done-card',
    icon('check'),
    h('div', h('div.strong', `Batch #${b.id} finished — ${plural(printed, 'leaf', 'leaves')} recorded as printed.`),
      h('div.sub', f.back ? `${plural(f.back, 'cheque')} came back to the tray (misprinted or not sent) and ${f.back === 1 ? 'is' : 'are'} at the top of the grid. A new run starts with its own first leaf again.` : 'Every cheque in the run printed.')),
    h('button.btn-link', { type: 'button', onclick: () => { TrayPage.finished = null; route(); } }, 'dismiss'));
}

document.addEventListener('keydown', ev => {
  if (Router.place !== 'tray' || ev.key !== 'Enter' || $$('dialog').some(d => d.open)) return;
  if (document.activeElement && /INPUT|TEXTAREA|SELECT|BUTTON/.test(document.activeElement.tagName)) return;
  const b = State.desk && State.desk.batch;
  if (b && b.status === 'rest_sent') { const btn = $('.verdict-bar .btn-primary'); if (btn && !btn.disabled) btn.click(); }
});
