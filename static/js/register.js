/* The Register: what each leaf became.
 *
 * Every leaf, newest first by when it was written, read from /api/register — where
 * gaps (leaves used but never recorded) and duplicate numbers are worked out,
 * so this page never guesses. A drawer shows one leaf: redrawn on the live
 * calibration, its stamp, its event trail, and the corrections the register
 * allows — the bookkeeping fields, and void. Rows are never deleted; a void
 * keeps the row and adds an event, and a used leaf number is never reused.
 *
 * Cheque books live here too: the numbered ranges the next leaf comes from.
 */
'use strict';

const RegPage = { data: null, filter: 'all', fy: '', q: '', entity: null, open: null, loading: false, selecting: false, sel: new Set() };

const STAMP = {
  printed: ['PRINTED', 'ok'], logged: ['BY HAND', 'ink'], queued: ['AT PRINTER', 'printer'],
  failed: ['SPOILED', 'bad'], void: ['VOID', 'bad'], blank: ['VOID', 'bad'],
};
const STATUS_TEXT = { printed: 'printed', logged: 'handwritten', queued: 'at the printer', failed: 'misprinted (spoiled)', void: 'void', blank: 'blank leaf, void' };

async function loadRegister() {
  RegPage.loading = true;
  try { RegPage.data = await api('/api/register'); }
  catch (err) { toast(err.message, true); }
  RegPage.loading = false;
}

Places.register = {
  title: 'Register',
  async render(root, params) {
    RegPage.entity = params.sub !== 'books' && params.entity ? Number(params.entity) : null;
    const tabs = h('div.tabs', { role: 'tablist' },
      h('a.tab' + (params.sub !== 'books' ? '.is-on' : ''), { href: '#/register', role: 'tab', 'aria-selected': String(params.sub !== 'books') }, 'Leaves'),
      h('a.tab' + (params.sub === 'books' ? '.is-on' : ''), { href: '#/register/books', role: 'tab', 'aria-selected': String(params.sub === 'books') }, 'Cheque books'));
    const body = h('div.reg-body', h('p.empty', 'Loading the register…'));
    appendTo(root, tabs, body);
    await loadRegister();
    if (!RegPage.data || !body.isConnected) return;
    body.replaceChildren();
    if (params.sub === 'books') renderBooks(body, params);
    else renderLeaves(body, params);
    hydrateIcons(body);
  },
  refresh() { /* the register reloads when it is opened or changed here */ },
};

/* ------------------------------------------------------------- leaves */
function rowMatches(r) {
  const f = RegPage.filter;
  if (r.kind === 'gap') return (f === 'all' || f === 'gaps') && !RegPage.fy && !RegPage.q && !RegPage.entity;
  if (f === 'gaps') return false;
  if (RegPage.entity && r.entity_id !== RegPage.entity) return false;
  if (f === 'printed' && r.status !== 'printed') return false;
  if (f === 'logged' && r.status !== 'logged') return false;
  if (f === 'spoiled' && !['failed', 'void', 'blank'].includes(r.status)) return false;
  if (f === 'queued' && r.status !== 'queued') return false;
  if (RegPage.fy && (!r.cheque_date || String(fyOf(r.cheque_date)) !== RegPage.fy)) return false;
  if (RegPage.q) {
    const hay = fold([r.cheque_no, r.payee, r.entity_name, r.memo, r.reason].join(' '));
    if (!hay.includes(fold(RegPage.q))) return false;
  }
  return true;
}

function renderLeaves(root, params) {
  const data = RegPage.data;
  const books = data.books;
  const inUse = books.books.find(b => b.id === books.in_use);
  const all = data.rows.filter(r => r.kind !== 'gap');
  const counts = { printed: 0, logged: 0, spoiled: 0, queued: 0 };
  const scope = inUse ? all.filter(r => r.book_id === inUse.id) : all;
  scope.forEach(r => {
    if (r.status === 'printed') counts.printed++;
    else if (r.status === 'logged') counts.logged++;
    else if (r.status === 'queued') counts.queued++;
    else counts.spoiled++;
  });
  const missing = data.rows.filter(r => r.kind === 'gap' && (!inUse || r.book_id === inUse.id)).reduce((s, r) => s + r.count, 0);
  const statLine = [`${counts.printed} printed`, `${counts.logged} handwritten`, `${counts.spoiled} spoiled/void`,
                    counts.queued ? `${counts.queued} at printer` : '', missing ? `${missing} missing` : ''].filter(Boolean).join(' · ');

  const fys = [...new Set(all.filter(r => r.cheque_date).map(r => fyOf(r.cheque_date)))].sort((a, b) => b - a);
  const fySel = h('select', { 'aria-label': 'Financial year' }, h('option', { value: '' }, 'All years'),
    fys.map(y => h('option', { value: String(y), selected: RegPage.fy === String(y) }, fyLabel(y))));
  fySel.addEventListener('change', () => { RegPage.fy = fySel.value; paintList(); });
  const search = h('input', { type: 'search', placeholder: 'Leaf, payee, note', value: RegPage.q, 'aria-label': 'Search the register' });
  search.addEventListener('input', debounce(() => { RegPage.q = search.value; paintList(); }, 150));
  const filters = seg('Show', [['all', 'All leaves'], ['printed', 'Printed'], ['logged', 'Handwritten'], ['spoiled', 'Spoiled & void'],
                               ['queued', 'At printer'], ['gaps', 'Gaps']], RegPage.filter, v => { RegPage.filter = v; paintList(); });

  const list = h('div.leaves', { role: 'list' });
  const drawer = h('aside.drawer', { hidden: true });
  const selBar = h('div.sel-bar', { hidden: true });
  const selToggle = h('button.btn.btn-quiet.btn-sm', { type: 'button', 'aria-pressed': String(RegPage.selecting),
    onclick: () => { RegPage.selecting = !RegPage.selecting; RegPage.sel.clear(); selToggle.setAttribute('aria-pressed', String(RegPage.selecting)); paintList(); } },
    icon('check'), 'Select');
  appendTo(root, 
    RegPage.entity ? entityHead(all) : h('div.page-head',
      h('div',
        h('div.eyebrow', inUse ? `Cheque book ${inUse.name} · leaves ${inUse.first_no}–${inUse.last_no}` : `${plural(all.length, 'leaf', 'leaves')} in the register`),
        h('h2.headline', inUse ? `${plural(inUse.left, 'leaf', 'leaves')} left in this book.` : 'Every leaf this desk has committed.'),
        h('p.lede', statLine)),
      inUse ? h('div.head-meter', bookMeter(inUse)) : h('a.btn.btn-quiet.btn-sm', { href: '#/register/books?add=1' }, icon('book'), 'Set up cheque books')),
    h('div.filter-bar', filters, fySel, search, selToggle, h('a.btn.btn-quiet.btn-sm', { href: '/api/export.csv', download: '' }, 'CSV')),
    h('div.reg-split', h('div', list, selBar), drawer,
      // Shown in the drawer's place on wide screens, so the column is there
      // before a leaf is chosen and the list never jumps when one is.
      h('aside.drawer-idle', h('p.strong', 'Choose a leaf'),
        h('p.sub', 'Its cheque, the leaf as printed, and what can still be done: printed or void, and its note.'))));

  function paintList() {
    const rows = data.rows.filter(rowMatches);
    list.replaceChildren(...rows.map(r => r.kind === 'gap' ? gapRow(r) : leafRow(r)));
    if (!list.children.length) list.appendChild(h('p.empty', 'No leaves match.'));
    list.classList.toggle('is-selecting', RegPage.selecting);
    paintSelBar();
    hydrateIcons(list);
  }

  /* Selecting cheques to count them against another entity — say, folding
     GST cheques that each became their own entity into one. */
  function paintSelBar() {
    const n = RegPage.sel.size;
    selBar.hidden = !RegPage.selecting;
    if (!RegPage.selecting) return;
    clear(selBar,
      h('span.strong', n ? `${plural(n, 'cheque')} selected` : 'Tick the cheques to move'),
      h('span.tool-push'),
      h('button.btn.btn-quiet.btn-sm', { type: 'button', disabled: !n, onclick: () => { RegPage.sel.clear(); paintList(); } }, 'Clear'),
      h('button.btn.btn-primary.btn-sm', { type: 'button', disabled: !n, onclick: async () => {
        const rows = data.rows.filter(r => r.kind === 'cheque' && RegPage.sel.has(r.id));
        if (await moveToEntity(rows)) { RegPage.sel.clear(); RegPage.selecting = false; route(); }
      } }, 'Move to entity…'));
  }

  function leafRow(r) {
    const [stamp, tone] = STAMP[r.status] || [r.status.toUpperCase(), 'ink'];
    const pick = RegPage.selecting && r.kind === 'cheque'
      ? h('input.lr-pick', { type: 'checkbox', checked: RegPage.sel.has(r.id), 'aria-label': `Select leaf ${r.cheque_no || r.id}` }) : null;
    if (pick) {
      pick.addEventListener('click', ev => ev.stopPropagation());
      pick.addEventListener('change', () => { if (pick.checked) RegPage.sel.add(r.id); else RegPage.sel.delete(r.id); paintSelBar(); });
    }
    const row = h('div.leaf-row' + (RegPage.open && RegPage.open.key === rowKey(r) ? '.is-open' : '') + (['void', 'failed', 'blank'].includes(r.status) ? '.is-dead' : ''),
      { role: 'listitem', tabIndex: 0 },
      RegPage.selecting ? h('div.lr-sel', pick) : null,
      h('div.lr-no', h('div.mono', r.cheque_no || '—'), h('div.sub', r.cheque_date ? fmtShort(r.cheque_date) : fmtShort((r.created_at || '').slice(0, 10)))),
      h('div.lr-who', h('div.strong', r.kind === 'blank' ? 'Blank leaf' : r.payee),
        h('div.sub', r.kind === 'blank' ? r.reason : [r.entity_name && r.entity_name !== r.payee ? r.entity_name : '', r.memo].filter(Boolean).join(' · '))),
      h('div.lr-flags', r.duplicate ? h('span.tag.tone-late', 'duplicate number') : null),
      h('div.lr-amt.mono', r.amount_paise ? rupees(r.amount_paise, { always: true }) : '—'),
      h('div.lr-stamp', h('span.stamp.tone-' + tone, stamp)));
    const open = () => openDrawer(r, drawer, paintList);
    row.addEventListener('click', open);
    row.addEventListener('keydown', ev => { if (ev.key === 'Enter') open(); });
    return row;
  }

  function gapRow(r) {
    return h('div.gap-row' + (r.sure ? '' : '.is-maybe'), { role: 'listitem' },
      icon('alert'),
      h('div', h('div.strong', `${plural(r.count, 'leaf', 'leaves')} unaccounted: ${r.from_no}${r.count > 1 ? ' – ' + r.to_no : ''}`),
        h('div.sub', r.sure ? 'Physically used but not in the register. Record them as handwritten or void.'
          : 'Possibly skipped — no cheque book covers these numbers. If they were used, record them; if they were never yours, ignore this.')),
      h('button.btn.btn-gold.btn-sm', { type: 'button', onclick: () => accountFor(r) }, 'Account for them'));
  }

  paintList();
  const want = params.leaf || params.cheque;
  if (want) {
    const r = data.rows.find(x => x.kind !== 'gap' && (params.cheque ? String(x.id) === params.cheque && x.kind === 'cheque' : x.cheque_no === params.leaf));
    if (r) openDrawer(r, drawer, paintList, { focusVoid: params.void === '1' });
    else toast(`Leaf ${want} is not in the register.`, true);
  }
}

/* The head when the register is opened for one entity, from its row on the Desk. */
function entityHead(all) {
  const id = RegPage.entity;
  const e = entityById(id);
  const mine = all.filter(r => r.entity_id === id);
  const label = e ? entityLabel(e) : (mine[0] && mine[0].entity_name) || 'this entity';
  const issued = mine.filter(r => ['printed', 'logged', 'queued'].includes(r.status));
  const total = issued.reduce((s, r) => s + Number(r.amount_paise || 0), 0);
  const last = issued.map(r => r.cheque_date).filter(Boolean).sort().pop();
  const spoiled = mine.length - issued.length;
  const head = h('div.page-head',
    h('div',
      h('div.eyebrow', `Entity · ${label}`),
      h('h2.headline', mine.length ? `${plural(mine.length, 'leaf', 'leaves')} written for ${label}.` : `No leaves written for ${label} yet.`),
      h('p.lede', [issued.length ? `${rupees(total, { whole: true })} issued` : '', last ? `last ${fmtDay(last)}` : '',
                   spoiled ? `${spoiled} spoiled/void` : ''].filter(Boolean).join(' · '))),
    h('div.head-acts',
      e ? h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: async () => { await editEntity(e); route(); } }, 'Edit entity') : null,
      e ? h('button.btn.btn-quiet.btn-sm', { type: 'button', title: 'Add a row for this entity to the Tray',
            onclick: () => writeInTray({ entity: e }) }, icon('pen'), 'Write a cheque') : null,
      h('a.btn.btn-quiet.btn-sm', { href: '#/register' }, 'All leaves')));
  return e ? h('div', head, entitySummary(e, issued)) : head;
}

/* What the desk knows about paying this entity: its settings, its rhythm and
   its last cheques on one scale. Read-only; writing a cheque is a button. */
function entitySummary(e, issued) {
  const r = rhythmOf(e.id);
  const recent = issued.filter(c => c.cheque_date).sort((a, b) => a.cheque_date.localeCompare(b.cheque_date)).slice(-12);
  const amounts = recent.map(c => Number(c.amount_paise)).sort((a, b) => a - b);
  const mid = amounts.length >> 1;
  const median = !amounts.length ? 0 : amounts.length % 2 ? amounts[mid] : Math.round((amounts[mid - 1] + amounts[mid]) / 2);
  const lines = [];
  if (r && r.next_due && e.kind !== 'one_off') {
    const dd = r.days_until_due;
    lines.push(`Next due ${fmtDay(r.next_due)}${dd < 0 ? `, ${plural(-dd, 'day')} late` : dd === 0 ? ', today' : `, in ${plural(dd, 'day')}`}.`);
  }
  if (e.date_rule && e.date_rule !== 'today') lines.push(`Cheques are ${ruleText(e.date_rule)}.`);
  return h('section.panel.ent-sum',
    h('div',
      h('div.eyebrow', 'Entity'),
      h('h3', entityLabel(e)),
      h('p.sub', [isPerCheque(e) ? `pays ${e.payee_prefix ? e.payee_prefix + ' …' : 'a payee typed per cheque'}` : `pays ${e.payee_name}`,
                  e.category, e.kind === 'one_off' ? 'one-off' : r ? r.cadence : '',
                  e.ac_payee ? 'A/C payee by default' : 'not crossed by default'].filter(Boolean).join(' · ')),
      lines.length ? h('div.mem-callout', lines.join(' ')) : null,
      e.notes ? h('p.sub', e.notes) : null),
    recent.length ? historyBars({ cheques: recent, median_paise: median }, 0) : h('p.sub', 'No cheques issued yet.'));
}

function rowKey(r) { return `${r.kind}:${r.id}`; }

/* Count cheques against another entity. The leaves are unchanged, so the
   entity has to be one that would have printed them: the same payee name,
   or one whose payee is typed per cheque. All move, or none do. */
async function moveToEntity(rows) {
  const fits = e => e.kind === 'per_cheque' || rows.every(r => r.payee === e.payee_name);
  const options = State.entities.slice().sort((a, b) => entityLabel(a).localeCompare(entityLabel(b)));
  const pick = h('select', { 'aria-label': 'Move to entity' },
    h('option', { value: '' }, 'Choose an entity…'),
    options.map(e => h('option', { value: String(e.id), disabled: !fits(e) },
      `${entityLabel(e)}${e.kind === 'per_cheque' ? ' · payee per cheque' : ''}${fits(e) ? '' : ' — prints a different name'}`)));
  const note = h('p.field-note');
  pick.addEventListener('change', () => {
    const e = entityById(Number(pick.value));
    note.textContent = !e ? '' : `${plural(rows.filter(r => r.entity_id !== e.id).length, 'cheque')} will be counted against ${entityLabel(e)}.`;
  });
  let moved = null;
  await modal({
    eyebrow: `Register · ${plural(rows.length, 'cheque')} selected`, title: 'Move to entity', wide: true,
    body: h('div',
      h('div.move-list', rows.map(r => h('div.move-row',
        h('span.mono.sub', r.cheque_no || '—'),
        h('div', h('div.mono', r.payee), h('div.sub', `now under ${r.entity_name || '—'}`)),
        h('span.mono', rupees(r.amount_paise, { always: true }))))),
      field('Move to', pick, note),
      h('ul.move-rules',
        h('li', 'Payee, amount, date and status stay exactly as printed.'),
        h('li', 'One event per cheque records the entity it moved from.'),
        h('li', 'Only into a payee-per-cheque entity, or one that prints the same name.'),
        h('li', 'An entity left with no cheques stays in the list; remove it from the Desk if it is no longer wanted.'))),
    actions: [{ label: 'Cancel', value: 'cancel' },
      { label: 'Move them', primary: true, value: 'move', onClick: async (ev, dlg) => {
        ev.preventDefault();
        if (!pick.value) { note.textContent = 'Choose the entity to move them to.'; return; }
        try {
          moved = await post('/api/cheques/move', { cheque_ids: rows.map(r => r.id), entity_id: Number(pick.value) });
          dlg.close('move');
        } catch (err) { note.textContent = err.message; }
      } }],
  });
  if (!moved) return false;
  toast(`${plural(moved.moved.length, 'cheque')} moved to ${entityLabel(entityById(Number(pick.value)))}.`);
  await loadDesk();
  return true;
}

async function openDrawer(r, drawer, repaint, opts = {}) {
  RegPage.open = { key: rowKey(r) };
  repaint();
  drawer.hidden = false;
  clear(drawer, h('p.empty', 'Loading…'));
  let c = null, events = [];
  try {
    if (r.kind === 'cheque') { c = await api(`/api/cheques/${r.id}`); events = c.events; }
    else events = (await api(`/api/leaves/${encodeURIComponent(r.cheque_no)}`)).events;
  } catch (err) { clear(drawer, h('p.empty', err.message)); return; }
  const [stamp, tone] = STAMP[r.status] || ['', ''];
  const view = makeLeaf({ small: true });
  if (c) {
    const e = entityById(c.entity_id);
    composeFor({ payee: c.payee, amount: c.amount_plain, cheque_date: c.cheque_date, crossing: e ? e.ac_payee !== false : true })
      .then(res => view.draw({ payee: c.payee, cheque_date: c.cheque_date, amount_words: c.amount_words,
                               amount_figures: res.ok ? res.amount_figures : '', crossing: e ? e.ac_payee !== false : true }));
  }
  view.setStamp(stamp, tone);
  const voidEv = [...events].reverse().find(e => e.kind === 'void');
  const close = h('button.icon-btn', { type: 'button', 'aria-label': 'Close', onclick: () => { drawer.hidden = true; RegPage.open = null; repaint(); } }, icon('x'));

  /* The two outcomes of a leaf, as two buttons: green says it printed as
     intended, red takes it out of the register as a payment. While a leaf is
     still at the printer, red is the misprint verdict (it also cancels the
     job); once it has gone out, red is a void, with the reason written down.
     A misprinted or void leaf already counts for nothing, so it has neither. */
  const actions = h('div.drawer-acts');
  let voidBox = null;
  if (c && c.status === 'queued' && c.batch_id == null) {
    appendTo(actions,
      h('button.btn.btn-ok.btn-sm', { type: 'button', onclick: () => regVerdict(c, 'printed') }, icon('check'), 'Printed correctly'),
      h('button.btn.btn-danger.btn-sm', { type: 'button', onclick: () => regVerdict(c, 'failed') }, icon('x'), 'Misprinted'));
  } else if (c && c.status === 'queued') {
    appendTo(actions, h('a.btn.btn-quiet.btn-sm', { href: '#/tray' }, `Confirm it from batch #${c.batch_id} in the tray`));
  } else if (c && (c.status === 'printed' || c.status === 'logged')) {
    voidBox = voidForm(c, r, drawer, repaint);
    const btn = h('button.btn.btn-danger.btn-sm', { type: 'button', 'aria-expanded': String(!voidBox.hidden),
      onclick: () => { voidBox.hidden = !voidBox.hidden; btn.setAttribute('aria-expanded', String(!voidBox.hidden));
                       if (!voidBox.hidden) $('textarea', voidBox).focus(); } },
      icon('x'), 'Void');
    appendTo(actions, btn);
  }
  // Paperwork, not an outcome: kept apart from the buttons so it never reads
  // as a second way to approve the cheque.
  // Post-dated is a fact about the record, not a state: one line in the trail.
  // The leaf itself is listed, reported and charted under the date it carries.
  const pdcNote = c && r.pdc && c.created_at ? h('li.ev-postdated',
    h('div.ev-top', h('span.strong', 'post-dated'), h('span.sub', fmtStamp(c.created_at))),
    h('div.sub', `Written ${fmtDay(c.created_at.slice(0, 10))}, dated ${fmtDay(c.cheque_date)}`)) : null;
  const editLink = c ? h('button.link-btn.drawer-edit', { type: 'button', onclick: () => editCheque(c) }, icon('edit'), 'Edit cheque number') : null;

  clear(drawer,
    h('div.drawer-head', h('span.eyebrow', r.kind === 'blank' ? `Leaf ${r.cheque_no} · blank` : `Leaf ${r.cheque_no || '— (none)'}`), close),
    h('div.mat.mat-small', view.el),
    c ? dl([
      ['Status', STATUS_TEXT[c.status]],
      ['Pay to', c.payee],
      c.entity_name && c.entity_name !== c.payee ? ['Entity', c.entity_name] : null,
      ['Amount', rupees(c.amount_paise, { always: true })],
      ['Date', fmtDay(c.cheque_date)],
      c.memo ? ['Note', c.memo] : null,
      c.batch_id ? ['Batch', `#${c.batch_id}`] : null,
      ['Calibration', `v${c.cal_version}`],
    ]) : dl([['Status', 'blank leaf, void'], ['Reason', r.reason || '—'], ['Recorded', fmtStamp(r.created_at)]]),
    editLink,
    voidEv ? h('div.void-reason', h('div.eyebrow.tone-late', 'Void reason'), h('p', voidEv.detail || 'no reason given')) : null,
    actions,
    voidBox,
    h('details.trail-fold',
      h('summary.eyebrow', `Event trail (${events.length + (pdcNote ? 1 : 0)})`),
      h('ol.trail', pdcNote, events.map(e => h('li.ev-' + e.kind.replace(/[^a-z-]/g, ''),
        h('div.ev-top', h('span.strong', e.kind), h('span.sub', fmtStamp(e.at))),
        e.detail ? h('div.sub', e.detail) : null))),
      h('p.foot', 'Register rows are never deleted. A void keeps the row and adds an event, and a spoiled leaf’s number is never reused.')));
  hydrateIcons(drawer);
  // Below the two-column width the record is a sheet over the page: it takes
  // focus when it opens and Escape puts it away.
  if (window.matchMedia('(max-width: 1399px)').matches) {
    drawer.tabIndex = -1;
    drawer.onkeydown = ev => { if (ev.key === 'Escape') close.click(); };
    if (!(opts.focusVoid && voidBox)) close.focus();
  }
  if (opts.focusVoid && voidBox) $('textarea', voidBox).focus();
}

function voidForm(c, r, drawer, repaint) {
  const reason = h('textarea', { rows: 2, maxlength: '200', placeholder: 'What was wrong: pick one above, then add the details' });
  const box = h('div.void-box', { hidden: !location.hash.includes('void=1') },
    h('p.sub', c.status === 'queued'
      ? 'Voiding a cheque still at the printer also cancels its print job, so it cannot land on the next leaf loaded.'
      : 'The row stays in the register with this reason, an event is written, and the leaf number is never used again. Voiding records that the cheque will not be paid — tell the payee, and the bank if it has been handed over.'),
    h('div.void-reasons', { role: 'group', 'aria-label': 'Common reasons' },
      ['Misprint', 'Signature', 'Incorrect amount', 'Other details'].map(t => h('button.chip-btn', { type: 'button',
        onclick: () => { reason.value = reason.value.trim() ? `${t}: ${reason.value.replace(/^(Misprint|Signature|Incorrect amount|Other details):\s*/, '')}` : `${t}: `; reason.focus(); } }, t))),
    field('Reason for the register', reason),
    h('button.btn.btn-danger.btn-sm', { type: 'button', onclick: async () => {
      try {
        await post(`/api/cheques/${c.id}/void`, { reason: reason.value.trim() });
        toast(`Leaf ${c.cheque_no || ''} voided.`);
        await Promise.all([loadRegister(), loadDesk()]);
        RegPage.open = null;
        route();
      } catch (err) { toast(err.message, true); }
    } }, 'Void this cheque'));
  return box;
}

async function regVerdict(c, outcome) {
  try {
    await post(`/api/cheques/${c.id}/confirm`, { outcome });
    toast(outcome === 'printed' ? 'Recorded as printed.' : 'Recorded as misprinted.');
    await Promise.all([loadRegister(), loadDesk()]);
    route();
  } catch (err) { toast(err.message, true); }
}

/* Only the bookkeeping fields: the leaf number, the note, and — among
   entities printing the same name — which one it counts against. */
async function editCheque(c) {
  const no = h('input', { type: 'text', value: c.cheque_no || '', maxlength: '40' });
  const memo = h('input', { type: 'text', value: c.memo || '', maxlength: '200' });
  const siblings = entitiesPrinting(c.payee);
  const ent = h('select', siblings.map(e => h('option', { value: String(e.id), selected: e.id === c.entity_id }, e.entity_name || '(no entity name)')));
  const choice = await modal({
    eyebrow: `Leaf ${c.cheque_no || '—'} · ${c.payee} · ${rupees(c.amount_paise, { always: true })}`,
    title: 'Correct a register entry',
    body: h('div', field('Leaf number', no), field('Note', memo), siblings.length > 1 ? field('Entity', ent) : null,
      h('p.sub', 'The payee, amount, date and status describe what was actually issued and cannot be changed here. Correct those by voiding the entry and writing a new one. Every change is written to the event trail with what it was before.')),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Save', primary: true, value: 'save' }],
  });
  if (choice !== 'save') return;
  try {
    const res = await api(`/api/cheques/${c.id}`, { method: 'PATCH', body: {
      cheque_no: no.value.trim(), memo: memo.value.trim(), entity_id: siblings.length > 1 ? Number(ent.value) : null } });
    toast(res.changed ? 'Register corrected.' : 'Nothing changed.');
    await Promise.all([loadRegister(), loadDesk()]);
    route();
  } catch (err) { toast(err.message, true); }
}

/* Leaves the register has no record of: say what became of each. */
async function accountFor(gap) {
  const rows = gap.numbers.map(n => {
    const mode = { v: 'void' };
    const reason = h('input', { type: 'text', placeholder: 'torn, spoiled by hand, lost…', maxlength: '200' });
    const payee = h('input', { type: 'text', list: 'payeeNames', placeholder: 'Payee as written', maxlength: '120' });
    const amount = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0.00' });
    const date = h('input', { type: 'date', value: todayISO() });
    const memo = h('input', { type: 'text', placeholder: 'Note', maxlength: '200' });
    const hand = h('div.af-hand', { hidden: true }, payee, amount, date, memo);
    const voidBox = h('div.af-void', reason);
    const sw = seg(`Leaf ${n}`, [['void', 'Void · blank'], ['logged', 'Written by hand']], 'void', v => {
      mode.v = v; hand.hidden = v !== 'logged'; voidBox.hidden = v !== 'void';
    });
    return { n, mode, reason, payee, amount, date, memo, el: h('div.af-row', h('span.mono', n), sw, voidBox, hand) };
  });
  const names = h('datalist', { id: 'payeeNames' }, [...new Set(State.entities.map(e => e.payee_name))].map(p => h('option', { value: p })));
  const err = h('p.form-error', { hidden: true });
  await modal({
    eyebrow: `${plural(gap.count, 'leaf', 'leaves')} · ${gap.from_no}${gap.count > 1 ? ' – ' + gap.to_no : ''}`,
    title: 'Account for these leaves', wide: true,
    body: h('div', h('p.sub', 'A leaf written by hand becomes a handwritten cheque with what was written on it. A leaf used up blank — torn, spoiled, lost — is recorded as void. Either way it counts as used and its number is never printed on again.'),
      names, h('div.af-list', rows.map(r => r.el)), err),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Record them', primary: true, value: 'save', onClick: async (ev, dlg) => {
      ev.preventDefault();
      const items = rows.map(r => r.mode.v === 'void'
        ? { cheque_no: r.n, as: 'void', reason: r.reason.value.trim() }
        : { cheque_no: r.n, as: 'logged', payee: r.payee.value.trim(), amount: r.amount.value.trim(),
            cheque_date: r.date.value, memo: r.memo.value.trim(),
            entity_id: (entitiesPrinting(r.payee.value).length === 1 ? entitiesPrinting(r.payee.value)[0].id : null) });
      try {
        const res = await post('/api/register/account-for', { items });
        toast(`${plural(res.logged.length + res.voided.length, 'leaf', 'leaves')} recorded.`);
        dlg.close('save');
        await Promise.all([loadRegister(), loadDesk()]);
        route();
      } catch (e2) { err.textContent = e2.message; err.hidden = false; }
    } }],
  });
}

/* ------------------------------------------------------------- books */
function renderBooks(root, params) {
  const v = RegPage.data.books;
  const books = v.books;
  const inUse = books.find(b => b.id === v.in_use);
  const shelf = books.filter(b => ['next', 'shelf', 'paused'].includes(b.state));
  const add = h('button.btn.btn-primary', { type: 'button', onclick: () => addBook() }, icon('plus'), 'Add a new cheque book');
  appendTo(root, 
    h('div.page-head',
      h('div',
        h('div.eyebrow', books.length ? `${plural(books.length, 'book')} · ${inUse ? '1 in use' : 'none in use'} · ${shelf.length} on the shelf` : 'No cheque books yet'),
        h('h2.headline', inUse ? `${inUse.name} is in use. ${plural(inUse.left, 'leaf', 'leaves')} left.` : books.length ? 'Every book is used up.' : 'Tell the desk about your cheque books.'),
        h('p.lede', books.length ? 'The next leaf is the next unused number in the book in use. A run stops at the end of a book and the next one starts on the next book, first leaf alone.'
          : 'Add the book you are using now — its used leaves are matched from the register by number, and nothing in the register is rewritten. Then add new books as they arrive.')),
      add),
    h('div.books-split',
      h('div.books', books.length ? books.map(bookCard) : h('p.empty', 'No books yet.')),
      h('aside.rules',
        rule('Numbers come from the book', 'The next leaf is the next unused number in the book in use, not ‘last cheque + 1’. A new book can start anywhere, even a different series.'),
        rule('Runs stop at the end of a book', 'If a tray would cross from one book into the next, the run takes the leaves left in this book, and the rest wait in the tray for the next book — whose first leaf prints alone again.'),
        rule('New-book alert at 80% used', 'When 80% of the book in use is consumed (printed, by hand, spoiled and void all count), the Desk alerts you. If no book is on the shelf, it says to order one.'),
        rule('Ranges never overlap', 'A new book is refused if any of its numbers is in another book, or already in the register — unless you say it is the book you are part-way through.'))));
  if (params.add === '1') setTimeout(() => addBook(), 50);
}

function rule(title, text) { return h('div.rule', h('div.strong', title), h('p.sub', text)); }

function bookCard(b) {
  const label = { finished: 'Finished', in_use: 'In use', next: 'Next', shelf: 'On the shelf', paused: 'Paused' }[b.state];
  const s = b.statuses;
  return h('div.book-card.state-' + b.state,
    h('div.book-top', h('span.strong', b.name), h('span.tag.state-' + b.state, label)),
    h('div.book-range.mono', `${b.first_no} – ${b.last_no}`),
    h('div.sub', `${b.count} leaves${b.state === 'in_use' ? ` · next leaf ${b.next_leaf}` : ''} · alert at ${b.alert_leaf}${b.received_on ? ` · received ${fmtDay(b.received_on)}` : ''}`),
    bookMeter(b),
    h('div.sub', [`${s.printed} printed`, `${s.logged} by hand`, `${s.spoiled} spoiled/void`, s.queued ? `${s.queued} at printer` : '',
                  b.missing.length ? `${b.missing.length} missing` : '', `${b.left} left`].filter(Boolean).join(' · ')),
    ['shelf', 'next', 'paused'].includes(b.state) ? h('div.btn-row', h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: async () => {
      try { await api(`/api/books/${b.id}`, { method: 'PATCH', body: { use_now: true } }); toast(`${b.name} is now the book in use.`); await loadDesk(); route(); }
      catch (err) { toast(err.message, true); }
    } }, 'Use this book now')) : null);
}

function nextBookName(books) {
  for (let c = 65; c < 91; c++) {
    const name = `Book ${String.fromCharCode(c)}`;
    if (!books.some(b => b.name.toLowerCase() === name.toLowerCase())) return name;
  }
  return `Book ${books.length + 1}`;
}

async function addBook() {
  const books = RegPage.data.books.books;
  const last = books[books.length - 1];
  const first = h('input.mono', { type: 'text', inputmode: 'numeric', maxlength: '40', placeholder: '000001',
                                  value: last && last.next_leaf === '' ? incrementNo(last.last_no) : '' });
  const lastNo = h('input.mono', { type: 'text', inputmode: 'numeric', maxlength: '40', placeholder: '000050' });
  const name = h('input', { type: 'text', maxlength: '40', value: nextBookName(books) });
  const received = h('input', { type: 'date', value: todayISO() });
  const used = h('input', { type: 'checkbox', checked: !books.length });
  const start = { v: 'after' };
  const count = h('div.big-count.mono', '—');
  const checks = h('ul.checks');
  const err = h('p.form-error', { hidden: true });
  const parse = s => { const m = /^(.*?)(\d+)$/.exec(s.trim()); return m ? { p: m[1], n: Number(m[2]), w: m[2].length } : null; };
  const paint = () => {
    const a = parse(first.value), z = parse(lastNo.value);
    const items = [];
    if (a && z && a.p === z.p && a.w === z.w && z.n >= a.n) {
      const n = z.n - a.n + 1;
      count.textContent = plural(n, 'leaf', 'leaves');
      const clash = books.find(b => { const bf = parse(b.first_no), bl = parse(b.last_no); return bf.p === a.p && a.n <= bl.n && bf.n <= z.n; });
      items.push(clash ? ['bad', `Overlaps ${clash.name} (${clash.first_no}–${clash.last_no})`] : ['ok', `No overlap with ${books.length ? books.map(b => b.name).join(', ') : 'any other book'}`]);
      const prev = books[books.length - 1];
      if (prev) {
        const pl = parse(prev.last_no);
        if (pl && pl.p === a.p && pl.n + 1 === a.n) items.push(['ok', `Follows straight on from ${prev.name} (${prev.last_no} → ${first.value.trim()})`]);
        else items.push(['muted', `A new series, not continuous with ${prev.name} — that is fine`]);
      }
      items.push(['ok', `Last is after first: ${first.value.trim()} → ${lastNo.value.trim()} is ${plural(n, 'leaf', 'leaves')}`]);
      if (n > 200) items.push(['warn', 'That is a lot of leaves for one book — check both numbers']);
      const ac = Math.ceil(n * 0.8);
      items.push(['warn', `New-book alert at leaf ${a.p}${String(a.n + ac - 1).padStart(a.w, '0')}, when 80% is used (${ac} of ${n})`]);
    } else {
      count.textContent = '—';
      if (first.value && lastNo.value) items.push(['bad', a && z && (a.p !== z.p || a.w !== z.w) ? 'The two numbers are from different series' : 'The last number must be after the first']);
    }
    clear(checks, items.map(([t, text]) => h('li.' + t, t === 'bad' ? icon('x') : t === 'warn' ? icon('bell') : icon('check'), text)));
  };
  first.addEventListener('input', paint);
  lastNo.addEventListener('input', paint);
  paint();
  const startSeg = h('div.start-opts',
    seg('Start using it', [['after', last ? `After ${last.name}` : 'When the others run out'], ['now', 'Now']], 'after', v2 => {
      start.v = v2;
      startNote.textContent = v2 === 'now' ? 'It is used next. Unused leaves in other books stay available but are skipped until you switch back.'
        : 'Recommended. It goes on the shelf behind the others, which are used first.';
    }));
  const startNote = h('p.field-note', 'Recommended. It goes on the shelf behind the others, which are used first.');
  await modal({
    eyebrow: 'Cheque books', title: 'Add a new cheque book', wide: true,
    body: h('div',
      h('p.sub', 'Type the numbers printed on the first and last leaves: the 6-digit number at the left of the MICR line. The leaf count is worked out from the two, counting both ends.'),
      h('div.field-row', field('First leaf number', first), field('Last leaf number', lastNo), h('div.field', h('div.label', 'Leaves (worked out)'), count)),
      h('div.field-row', field('Name', name), field('Received', received)),
      h('div.field', h('div.label', 'Start using it'), startSeg, startNote),
      h('label.switch', used, h('span', 'This is the book I am using now — some of its leaves are already in the register')),
      checks, err),
    onOpen: () => first.focus(),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Add to the shelf', primary: true, value: 'save', onClick: async (ev, dlg) => {
      ev.preventDefault();
      try {
        const res = await post('/api/books', { name: name.value.trim(), first_no: first.value.trim(), last_no: lastNo.value.trim(),
          received_on: received.value || null, start: start.v, already_used: used.checked });
        toast(`${name.value.trim()} added — ${plural(res.count, 'leaf', 'leaves')}${res.in_register ? `, ${res.in_register} already used` : ''}.`);
        dlg.close('save');
        await loadDesk();
        go('register', { sub: 'books' });
      } catch (e2) { err.textContent = e2.message; err.hidden = false; }
    } }],
  });
}
