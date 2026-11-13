/* The Desk: what needs paying now.
 *
 * A month (or quarter) river of entities placed by date around a NOW line:
 * what is due floats toward it, what is paid sits where it was paid. Leaves
 * at the printer waiting for a verdict are pinned at the top, and so are the
 * cheque-book alert and post-dated reminders. Everything here is read from
 * /api/desk; the only writes are verdicts (the existing confirm call),
 * reminder answers and the book alert answer, which are events.
 */
'use strict';

const DeskView = {
  scale: stored('banyan.desk.window', 'focus'),
  selected: new Set(),
  filter: '',
  show: '',
  remindedKey: '',
};

const TONE = { overdue: 'late', 'due-now': 'late', 'due-soon': 'soon', 'on-track': 'later' };

function deskWindow(today, scale) {
  const t = parseISO(today);
  if (scale === 'quarter') {
    const q = quarterOf(today);
    const fy = fyOf(today);
    const startMonth = 3 + (q - 1) * 3;           // Apr = 3
    const start = new Date(fy + (startMonth > 11 ? 1 : 0), startMonth % 12, 1);
    const end = new Date(start.getFullYear(), start.getMonth() + 3, 0);
    return { start: iso(start), end: iso(end), label: `Q${q} ${fyLabel(fy)}`, sub: `${MONTHS[start.getMonth()]}–${MONTHS[end.getMonth()]}` };
  }
  if (scale === 'focus') {
    // A week back for anything just paid or overdue, two weeks ahead: what an
    // association with a couple of dozen payees actually acts on. Overdue chips older
    // than the window stack at its left edge, as they do in the month view.
    const start = addDays(today, -7), end = addDays(today, 14);
    return { start, end, label: 'Two weeks ahead', sub: `${fmtShort(start)} to ${fmtShort(end)}` };
  }
  const start = new Date(t.getFullYear(), t.getMonth(), 1);
  const end = new Date(t.getFullYear(), t.getMonth() + 1, 0);
  return { start: iso(start), end: iso(end), label: MONTHS_LONG[t.getMonth()], sub: String(t.getFullYear()) };
}

/* Every chip on the river, with where it sits and what it says. */
function riverChips(desk, win) {
  const today = desk.today;
  const chips = [];
  const atPrinter = new Set();
  const inTray = new Set(State.tray.map(d => d.entity_id).filter(x => x != null));
  const pdcIds = new Set(desk.reminders.map(r => r.cheque_id));

  const queued = desk.awaiting.concat(desk.batch ? desk.batch.items.filter(i => i.status === 'queued')
    .map(i => ({ id: i.cheque_id, entity_id: i.entity_id, payee: i.payee, amount_paise: i.amount_paise,
                 cheque_date: i.cheque_date, cheque_no: i.cheque_no })) : []);
  queued.forEach(c => {
    atPrinter.add(c.entity_id);
    const e = entityById(c.entity_id);
    chips.push({ kind: 'printer', date: c.cheque_date, entity: e, name: e ? entityLabel(e) : c.payee,
                 sub: `${rupees(c.amount_paise, { whole: true })}  ·  at printer`, tone: 'printer' });
  });

  desk.recent.forEach(c => {
    if (c.status === 'queued' || pdcIds.has(c.id)) return;
    if (c.cheque_date < win.start || c.cheque_date > win.end) return;
    const e = entityById(c.entity_id);
    chips.push({ kind: 'paid', date: c.cheque_date, entity: e, name: e ? entityLabel(e) : c.payee,
                 sub: `${rupees(c.amount_paise, { whole: true })}  ·  paid ${fmtShort(c.cheque_date)}`, tone: 'paid' });
  });

  desk.reminders.forEach(r => {
    if (r.cheque_date < win.start || r.cheque_date > win.end) return;
    chips.push({ kind: 'pdc', date: r.cheque_date, entity: entityById(r.entity_id), name: r.entity_name,
                 sub: `${rupees(r.amount_paise, { whole: true })}  ·  PDC valid ${fmtShort(r.cheque_date)}`, tone: 'pdc' });
  });

  desk.rhythms.forEach(r => {
    if (r.kind === 'one_off' || !r.next_due || !TONE[r.status] || r.entity_id == null) return;
    if (atPrinter.has(r.entity_id) || r.next_due > win.end) return;
    const e = entityById(r.entity_id);
    if (!e) return;
    const d = r.days_until_due;
    const when = d < 0 ? `${-d}d late` : d === 0 ? 'today' : d === 1 ? 'tomorrow' : `in ${d}d`;
    chips.push({
      kind: 'due', date: r.next_due < win.start ? win.start : r.next_due, entity: e, rhythm: r,
      name: entityLabel(e), tone: TONE[r.status], selectable: true, inTray: inTray.has(e.id),
      sub: `${rupees(r.typical_paise, { whole: true })}  ·  ${r.cadence.replace('maybe ', '')} · ${when}`,
    });
  });
  return chips;
}

function dueEntities(desk, withinDays = 7) {
  return desk.rhythms.filter(r => r.kind !== 'one_off' && r.entity_id != null && r.days_until_due != null
    && ['overdue', 'due-now', 'due-soon'].includes(r.status) && r.days_until_due <= withinDays)
    .map(r => entityById(r.entity_id)).filter(Boolean);
}

function addToTray(entities, note) {
  const have = new Set(State.tray.map(d => d.entity_id));
  const added = entities.filter(e => !have.has(e.id));
  added.forEach(e => State.tray.push(draftFor(e)));
  saveTray();
  const skipped = entities.length - added.length;
  toast(`${plural(added.length, 'cheque')} added to the tray${skipped ? ` (${skipped} already there)` : ''}.` + (note || ''));
  return added.length;
}

function trayAllDue() {
  if (!State.desk) return;
  const due = dueEntities(State.desk);
  if (!due.length) { toast('Nothing is due this week.'); return; }
  addToTray(due);
  rerender();
}

/* ------------------------------------------------------------- verdicts */
async function settleSingle(c, outcome) {
  try {
    await post(`/api/cheques/${c.id}/confirm`, { outcome });
    toast(outcome === 'printed'
      ? `Leaf ${c.cheque_no || ''} recorded as printed.`
      : `Leaf ${c.cheque_no || ''} recorded as misprinted — it is spoiled and its number is never reused.`);
    await loadDesk();
  } catch (err) { toast(err.message, true); }
}

function verdictCard(c) {
  const e = entityById(c.entity_id);
  const mins = Math.max(0, Math.round((Date.now() - new Date(c.created_at)) / 60000));
  return h('div.verdict-card',
    h('div.eyebrow.tone-printer', 'At the printer · needs your verdict'),
    h('div.verdict-name', e ? entityLabel(e) : c.payee),
    h('div.verdict-sub', `Leaf ${c.cheque_no || '—'} · ${rupees(c.amount_paise, { always: true })} · sent ${mins < 60 ? `${mins} min` : fmtStamp(c.created_at)} ago`),
    h('div.btn-row',
      h('button.btn.btn-ok.btn-sm', { type: 'button', onclick: () => settleSingle(c, 'printed') }, icon('check'), 'Printed correctly'),
      h('button.btn.btn-danger-quiet.btn-sm', { type: 'button', onclick: () => settleSingle(c, 'failed') }, icon('x'), 'Misprinted')));
}

function batchCard(b) {
  const at = b.items.filter(i => i.status === 'queued');
  const text = { review: 'is ready for its first leaf', first_sent: 'has its first leaf at the printer',
                 first_ok: 'is waiting for the rest of its leaves', rest_sent: `has ${plural(at.length, 'leaf', 'leaves')} at the printer` }[b.status];
  return h('div.verdict-card',
    h('div.eyebrow.tone-printer', `Batch #${b.id} · ${plural(b.count, 'cheque')}`),
    h('div.verdict-name', `This run ${text}.`),
    h('div.btn-row', h('a.btn.btn-primary.btn-sm', { href: '#/tray' }, 'Go to the tray')));
}

/* ------------------------------------------------------------- books */
function bookPace(desk) {
  const since = addDays(desk.today, -60);
  const n = desk.recent.filter(c => c.cheque_date >= since && c.cheque_no).length;
  return n / 60 * 7;
}

function bookAlertCard(desk) {
  const a = desk.books.alert;
  if (!a) return null;
  const book = desk.books.books.find(b => b.id === a.book_id);
  if (!a.needs_answer) {
    return h('div.note-line', icon('book'),
      `${book.name} is ${Math.round(a.consumed / a.count * 100)}% used (${a.left} left) — ${a.next_book} is next on the shelf.`);
  }
  const pace = bookPace(desk);
  const weeks = pace > 0 ? Math.max(1, Math.round(a.left / pace)) : null;
  const answer = async (body, msg) => {
    try { await post(`/api/books/${a.book_id}/alert`, body); toast(msg); await loadDesk(); }
    catch (err) { toast(err.message, true); }
  };
  return h('div.alert-card',
    h('div.eyebrow.tone-soon', icon('bell'), `Cheque book · ${Math.round(a.consumed / a.count * 100)}% used`),
    h('div.alert-title', 'Order a new cheque book.'),
    h('p', `${book.name} (${book.first_no}–${book.last_no}) has ${a.consumed} of ${a.count} leaves used and there is no next book on the shelf.` +
      (weeks ? ` At about ${pace.toFixed(1)} leaves a week, the last ${a.left} last roughly ${plural(weeks, 'week')}.` : '')),
    bookMeter(book),
    h('div.btn-row',
      h('a.btn.btn-primary.btn-sm', { href: '#/register/books?add=1' }, icon('plus'), 'Add a cheque book'),
      h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: () => answer({ answer: 'ordered' }, 'Noted — the desk will not ask again for this book.') }, 'Already ordered'),
      h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: () => answer({ answer: 'snooze', days: 3 }, 'The desk will ask again in 3 days.') }, 'Remind me in 3 days')),
    h('p.foot', 'Shown each day until answered; the answer is written to the book’s events.'));
}

function bookMeter(b) {
  const used = b.consumed / b.count * 100;
  return h('div.meter', { title: `${b.consumed} of ${b.count} leaves used` },
    h('div.meter-fill', { style: { width: used + '%' } }),
    h('div.meter-mark', { style: { left: (b.alert_count / b.count * 100) + '%' }, title: `alert at leaf ${b.alert_leaf}` }),
    h('span.meter-label', bookAlertText(b)));
}

/* How far off the new-book alert is, in leaves, which is what the treasurer
   plans by; the leaf number stays in the tooltip. */
function bookAlertText(b) {
  const toGo = b.alert_count - b.consumed;
  if (toGo > 0) return `alert in ${toGo} ${toGo === 1 ? 'leaf' : 'leaves'}`;
  return 'alert reached';
}

/* ------------------------------------------------------------- reminders */
async function answerReminders(list, answer) {
  try {
    for (const r of list) await post(`/api/cheques/${r.cheque_id}/remind-ack`, { answer, days: 1 });
    toast(answer === 'ack' ? 'Noted — an event was written on each cheque.' : 'The desk will remind you tomorrow.');
  } catch (err) { toast(err.message, true); }
  await loadDesk();
}

async function stopPostDated(r) {
  const reason = h('textarea', { rows: 2, maxlength: '200' }, 'stopped before due date');
  const go2 = await modal({
    eyebrow: `Leaf ${r.cheque_no} · post-dated to ${fmtDay(r.cheque_date)}`,
    title: `Stop the cheque to ${r.entity_name}?`,
    body: h('div',
      h('p', 'Stopping a post-dated cheque is a void: the row stays in the register with its reason, an event is written, and the leaf number is never used again. Tell the payee — and the bank, if the cheque has already been handed over.'),
      field('Reason for the register', reason)),
    actions: [{ label: 'Keep it', value: 'no' }, { label: 'Void this cheque', danger: true, value: 'void' }],
  });
  if (go2 !== 'void') return;
  try {
    await post(`/api/cheques/${r.cheque_id}/void`, { reason: reason.value.trim() || 'stopped before due date' });
    toast(`Leaf ${r.cheque_no} voided.`);
    await loadDesk();
  } catch (err) { toast(err.message, true); }
}

async function showReminders(desk, force) {
  const due = desk.reminders.filter(r => r.due);
  if (!due.length) return;
  const key = due.map(r => `${r.cheque_id}:${r.stage}`).join(',');
  if (!force && (DeskView.remindedKey === key || sessionStorage.getItem('banyan.reminded') === key)) return;
  DeskView.remindedKey = key;
  try { sessionStorage.setItem('banyan.reminded', key); } catch (e) { /* convenience only */ }
  const first = due[0].cheque_date;
  const group = due.filter(r => r.cheque_date === first);
  const total = group.reduce((s, r) => s + r.amount_paise, 0);
  const days = group[0].days_until;
  const choice = await modal({
    className: 'reminder',
    eyebrow: `Reminder · ${days === 0 ? 'today' : `${plural(days, 'day')} to go`}`,
    title: `${group.length === 1 ? 'A post-dated cheque goes' : `${countWord(group.length)} post-dated cheques go`} live on ${fmtLong(first).replace(/ \d{4}$/, '')}`,
    body: h('div',
      h('div.remind-list', group.map(r => h('div.remind-row',
        h('span.mono', r.cheque_no || '—'),
        h('div', h('div.strong', r.entity_name), h('div.sub', `${r.memo || 'post-dated'} · written ${fmtShort(r.written_on)}`)),
        h('span.mono.amt', rupees(r.amount_paise, { always: true }))))),
      h('p.callout', `${rupees(total, { always: true })} must be in the account by ${days === 0 ? 'today' : fmtLong(addDays(first, -1)).replace(/ \d{4}$/, '')}.`),
      h('p.foot', 'This pops up when the desk opens, on each of the entity’s reminder days and again on the day. Answering it writes an event on each cheque; the cheque stays listed on the Desk until its date passes.')),
    actions: [
      { label: 'Stop one…', value: 'stop' },
      { label: 'Remind me tomorrow', value: 'snooze' },
      { label: 'Funds are arranged', primary: true, value: 'ack' },
    ],
  });
  if (choice === 'ack' || choice === 'snooze') await answerReminders(group, choice);
  if (choice === 'stop') {
    const which = group.length === 1 ? group[0] : await pickCheque(group);
    if (which) await stopPostDated(which);
  }
}

async function pickCheque(list) {
  let picked = null;
  await modal({
    title: 'Which cheque?',
    body: h('div.pick-list', list.map(r => h('button.pick', { type: 'submit', value: String(r.cheque_id),
      onclick: () => { picked = r; } }, h('span.mono', r.cheque_no), ` ${r.entity_name} · ${rupees(r.amount_paise, { always: true })}`))),
    actions: [{ label: 'Cancel', value: 'cancel' }],
  });
  return picked;
}

/* ------------------------------------------------------------- render */
Places.desk = {
  title: 'Desk',
  render(root) {
    const desk = State.desk;
    if (!desk) { root.appendChild(h('p.empty', 'Loading the desk…')); return; }
    const late = desk.rhythms.filter(r => r.kind !== 'one_off' && ['overdue', 'due-now'].includes(r.status));
    const week = desk.rhythms.filter(r => r.kind !== 'one_off' && r.status === 'due-soon' && r.days_until_due <= 7);
    const latePaise = late.reduce((s, r) => s + r.typical_paise, 0);
    const t = parseISO(desk.today);
    const fy = fyOf(desk.today);

    const headline = late.length || week.length
      ? `${late.length ? `${countWord(late.length)} payment${late.length === 1 ? '' : 's'} overdue` : 'Nothing overdue'}` +
        `${week.length ? `, ${countWord(week.length, true)} due this week` : ', nothing else due this week'}.`
      : 'Nothing is due this week.';
    const sub = late.length
      ? `${rupees(latePaise, { whole: true })} is behind the NOW line. Tick chips to tray them, or press T to tray everything due.`
      : week.length ? 'Tick chips to tray them, or press T to tray everything due this week.'
      : 'Entities appear here once they have a rhythm; the list below has everyone you pay.';

    const side = h('div.desk-side',
      desk.awaiting.map(verdictCard),
      desk.batch ? batchCard(desk.batch) : null,
      bookAlertCard(desk));

    appendTo(root, 
      h('section.desk-head',
        h('div',
          h('div.eyebrow', `${DAYS_LONG[t.getDay()]} ${t.getDate()} ${MONTHS_LONG[t.getMonth()]} · Q${quarterOf(desk.today)} ${fyLabel(fy)}`),
          h('h2.headline', headline),
          h('p.lede', sub)),
        side),
      riverPanel(desk),
      pdcStrip(desk),
      entityTable(desk),
      setupHint(desk));
    setTimeout(() => showReminders(desk), 300);
  },
  refresh() {
    const a = document.activeElement;
    if (a && $('#content').contains(a) && /INPUT|TEXTAREA|SELECT/.test(a.tagName)) return;
    route();
  },
};

function setupHint(desk) {
  if (desk.books.books.length || stored('banyan.hint.books', false)) return null;
  return h('div.note-line', icon('book'),
    'Set up your cheque books and the next leaf number comes from the book in use, with an alert when it runs low. ',
    h('a', { href: '#/register/books?add=1' }, 'Add the book you are using now'),
    h('button.btn-link', { type: 'button', onclick: ev => { store('banyan.hint.books', true); ev.target.closest('.note-line').remove(); } }, 'not now'));
}

function pdcStrip(desk) {
  if (!desk.reminders.length) return null;
  return h('section.panel.pdc-strip',
    h('div.panel-head', h('span.eyebrow', 'Post-dated · coming up'),
      desk.reminders.some(r => r.due) ? h('button.btn-link', { type: 'button', onclick: () => showReminders(desk, true) }, 'answer the reminder') : null),
    h('div.pdc-list', desk.reminders.map(r => h('div.pdc-item' + (r.due ? '.is-due' : ''),
      h('span.mono', r.cheque_no || '—'),
      h('span.strong', r.entity_name),
      h('span.mono', rupees(r.amount_paise, { always: true })),
      h('span.sub', `valid ${fmtShort(r.cheque_date)} · ${relDays(r.days_until)}${r.answered ? ' · funds arranged' : ''}`),
      h('button.btn-link', { type: 'button', onclick: () => stopPostDated(r) }, 'stop…')))));
}

function riverPanel(desk) {
  const win = deskWindow(desk.today, DeskView.scale);
  const chips = riverChips(desk, win);
  const days = daysBetween(win.start, win.end) + 1;
  const panel = h('section.panel.river-panel');
  const scaleSeg = seg('Scale', [['focus', 'Two weeks'], ['month', 'Month'], ['quarter', 'Quarter']], DeskView.scale, v => {
    DeskView.scale = v; store('banyan.desk.window', v); route();
  });
  const legend = h('div.legend',
    [['late', 'Overdue'], ['soon', 'Due soon'], ['later', 'Later'], ['paid', 'Paid'], ['printer', 'At printer'], ['pdc', 'Post-dated']]
      .map(([k, l]) => h('span.lg.tone-' + k, h('i'), l)));
  panel.appendChild(h('div.river-top', h('h3.river-title', win.label, h('span', ' ' + win.sub)), legend, scaleSeg));

  const axis = h('div.river-axis');
  const field2 = h('div.river-field');
  field2.style.backgroundSize = `calc(100% / ${days}) 24px`;  // one dot column per day
  const river = h('div.river', axis, field2);
  panel.appendChild(river);
  panel.appendChild(h('p.foot', 'Overdue chips stack at the left edge. Click a chip to open the entity; tick it to add it to the tray.'));

  const layout = () => {
    const width = field2.clientWidth;
    if (!width) return;
    const x = s => (daysBetween(win.start, s) + 0.5) / days * width;
    axis.replaceChildren();
    for (let i = 0; i < days; i++) {
      const d = addDays(win.start, i);
      const dd = parseISO(d);
      const weekend = dd.getDay() === 0 || dd.getDay() === 6;
      // Where days are too narrow for their numbers, label every other one
      // (today always), so the axis reads instead of running together.
      const roomy = width / days >= 22 || i % 2 === 0 || d === desk.today;
      if ((DeskView.scale !== 'quarter' && roomy) || (DeskView.scale === 'quarter' && (dd.getDay() === 1 || dd.getDate() === 1))) {
        axis.appendChild(h('span.tick' + (weekend ? '.weekend' : '') + (d === desk.today ? '.today' : ''),
          { style: { left: x(d) + 'px' } }, DeskView.scale !== 'quarter' ? String(dd.getDate()) : (dd.getDate() === 1 ? MONTHS[dd.getMonth()] : String(dd.getDate()))));
      }
    }
    field2.replaceChildren();
    const CW = Math.min(180, Math.max(132, width / 7)), CH = 46, GAP = 6;
    const lanes = [];
    chips.sort((a, b) => a.date.localeCompare(b.date) || (a.kind === 'due' ? -1 : 1));
    let maxLane = 0;
    chips.forEach(c => {
      const cx = x(c.date);
      const left = Math.max(0, Math.min(width - CW, cx - CW / 2));
      let lane = lanes.findIndex(end => end + GAP <= left);
      if (lane < 0) { lane = lanes.length; lanes.push(0); }
      lanes[lane] = left + CW;
      maxLane = Math.max(maxLane, lane);
      field2.appendChild(chipNode(c, { left: left + 'px', top: (lane * (CH + GAP) + 8) + 'px', width: CW + 'px' }));
    });
    field2.style.height = Math.max(DeskView.scale === 'focus' ? 68 : 160, (maxLane + 1) * (CH + GAP) + 16) + 'px';
    if (desk.today >= win.start && desk.today <= win.end) {
      field2.appendChild(h('div.now-line', { style: { left: x(desk.today) + 'px' } }, h('span', 'NOW')));
    }
    if (!chips.length) field2.appendChild(h('p.empty', 'Nothing due, paid or post-dated in this view.'));
  };
  new ResizeObserver(layout).observe(field2);
  return panel;
}

function chipNode(c, style) {
  const sel = c.selectable && DeskView.selected.has(c.entity.id);
  const n = h('div.chip-r.tone-' + c.tone + (sel ? '.is-selected' : '') + (c.inTray ? '.in-tray' : ''), { style },
    h('div.chip-name', c.name),
    h('div.chip-sub', c.sub + (c.inTray ? ' · in tray' : '')));
  if (c.entity) {
    n.tabIndex = 0;
    n.setAttribute('role', 'button');
    n.title = `Open ${c.name}: its leaves and history`;
    const open = () => go('register', { entity: c.entity.id });
    n.addEventListener('click', ev => { if (!ev.target.closest('.chip-tick')) open(); });
    n.addEventListener('keydown', ev => { if (ev.key === 'Enter') open(); if (ev.key === ' ' && c.selectable) { ev.preventDefault(); toggleSel(c.entity.id); } });
  }
  if (c.selectable) {
    const box = h('input.chip-tick', { type: 'checkbox', checked: sel, 'aria-label': `Select ${c.name} for the tray` });
    box.addEventListener('change', () => toggleSel(c.entity.id));
    n.appendChild(box);
  }
  return n;
}

function toggleSel(id) {
  if (DeskView.selected.has(id)) DeskView.selected.delete(id); else DeskView.selected.add(id);
  rerender();
  updateDock();
}

/* Everyone the association pays, including those with no rhythm yet. */
function entityTable(desk) {
  const box = h('section.panel.ent-panel');
  const list = h('div.ent-list');
  const search = h('input', { type: 'search', placeholder: 'Find an entity, payee or head', value: DeskView.filter,
                              'aria-label': 'Find an entity' });
  const show = h('select', { 'aria-label': 'Show' },
    [['', 'Everyone'], ['due', 'Due or overdue'], ['regular', 'Regulars'], ['one_off', 'One-offs'], ['unpaid', 'Never paid']]
      .map(([v, l]) => h('option', { value: v, selected: DeskView.show === v }, l)));
  const paint = () => {
    const q = fold(search.value);
    DeskView.filter = search.value;
    DeskView.show = show.value;
    const rows = desk.entities.filter(e => {
      if (q && !fold([e.entity_name, e.payee_name, e.category, e.notes].join(' ')).includes(q)) return false;
      const r = rhythmOf(e.id);
      if (show.value === 'due') return r && ['overdue', 'due-now', 'due-soon'].includes(r.status) && e.kind !== 'one_off';
      if (show.value === 'regular') return e.kind !== 'one_off';
      if (show.value === 'one_off') return e.kind === 'one_off';
      if (show.value === 'unpaid') return !e.count;
      return true;
    });
    list.replaceChildren(...rows.map(e => entityRow(e)));
    if (!rows.length) list.appendChild(h('p.empty', desk.entities.length ? 'No entity matches.' : 'No entities yet. Write a cheque to a new name, or add one.'));
  };
  search.addEventListener('input', debounce(paint, 120));
  show.addEventListener('change', paint);
  appendTo(box, 
    h('div.panel-head',
      h('h3', 'Everyone you pay', h('span.head-note', ` · ${plural(desk.entities.length, 'entity', 'entities')} — only the payee name is ever printed`)),
      h('div.tools', search, show,
        h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: () => writeInTray({ oneOff: true }) }, icon('pen'), 'Write a one-off'),
        h('button.btn.btn-quiet.btn-sm', { type: 'button', onclick: async () => { await editEntity(null); } }, icon('plus'), 'New entity'))),
    list);
  paint();
  return box;
}

function entityRow(e) {
  const r = rhythmOf(e.id);
  const usual = usualPaise(e);
  const due = r && r.next_due && e.kind !== 'one_off' && TONE[r.status]
    ? h('span.tag.tone-' + TONE[r.status], r.days_until_due < 0 ? `${-r.days_until_due}d late` : `due ${relDays(r.days_until_due)}`) : null;
  const inTray = State.tray.some(d => d.entity_id === e.id);
  // The row opens the register on this entity's leaves; its buttons keep their own jobs.
  return h('div.ent-row.is-link', { onclick: ev => { if (!ev.target.closest('button, a')) go('register', { entity: e.id }); } },
    h('div.ent-who',
      h('a.strong.ent-name', { href: `#/register?entity=${e.id}`, title: 'Show the leaves written for this entity' }, entityLabel(e)),
      h('div.sub', e.kind === 'per_cheque' ? `pays ${e.payee_prefix ? e.payee_prefix + ' …' : 'a payee typed per cheque'}` : `pays ${e.payee_name}`)),
    h('div.sub', e.category || '—'),
    h('div.sub', e.kind === 'one_off' ? 'one-off' : r ? r.cadence.replace('maybe ', 'maybe ') : (e.count ? `${e.count} cheque${e.count === 1 ? '' : 's'}` : 'never paid'),
      e.date_rule && e.date_rule !== 'today' ? h('span.tag.tone-pdc', 'PDC') : null),
    h('div.mono.num', usual ? rupees(usual, { whole: true }) : '—'),
    h('div.sub', due || (e.last_date ? `paid ${fmtShort(e.last_date)}` : '')),
    h('div.row-acts',
      h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => writeInTray({ entity: e }) }, 'Write'),
      h('button.btn.btn-quiet.btn-xs', { type: 'button', disabled: inTray, title: inTray ? 'Already in the tray' : 'Add to the tray',
        onclick: () => { addToTray([e]); rerender(); } }, inTray ? 'In tray' : '+ Tray'),
      h('button.btn-link', { type: 'button', onclick: () => editEntity(e) }, 'edit')));
}

/* ------------------------------------------------------------- dock */
on('dock', () => {
  const dock = $('#dock');
  const n = State.tray.length;
  const sel = DeskView.selected.size;
  const count = $('#trayCount');
  count.hidden = !n;
  count.textContent = String(n);
  if (Router.place === 'tray' || (!n && !sel)) { dock.hidden = true; return; }
  dock.hidden = false;
  const total = State.tray.reduce((s, d) => s + (d.amount_paise || 0), 0);
  const next = nextLeaf();
  // One line, always: what is waiting and what it adds up to, then the way in.
  // The next leaf number is detail for the Tray itself, kept in the tooltip.
  const what = n ? `${plural(n, 'cheque')} · ${rupees(total, { whole: true })}` : 'Nothing in the tray yet';
  clear(dock,
    h('span.dock-ico', { 'aria-hidden': 'true' }, icon('tray'), n ? h('b', String(n)) : null),
    h('div.dock-text', { title: next ? `Next leaf ${next}` : '' },
      h('span.dock-label', 'Tray'),
      h('span.dock-what', what),
      sel ? h('span.dock-sel', `+ ${sel} selected`) : null),
    sel ? h('button.btn.btn-quiet-dark.btn-sm', { type: 'button', onclick: () => {
      const picked = [...DeskView.selected].map(entityById).filter(Boolean);
      DeskView.selected.clear();
      addToTray(picked);
      rerender();
    } }, 'Add selected') : null,
    n ? h('a.btn.btn-primary.btn-sm', { href: '#/tray' }, 'Open tray') : null);
  hydrateIcons(dock);
});

on('tray', updateDock);
