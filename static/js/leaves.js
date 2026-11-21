/* Review heads: the treasurer's list of what members changed, and of what
 * still needs a head.
 *
 * Members change heads and notes from the reports, and the latest change
 * wins. This is where the desk sees those changes, tidies cheques with no
 * head, and re-heads several cheques at once. Every row opens the same panel
 * the reports use; nothing here edits in place.
 */
'use strict';

const SEEN_KEY = 'banyan.heads.seen';
const ReviewPage = { view: null, fy: null, quarter: null, data: null, changed: null, picked: new Set(), seenBefore: '' };

function seenAt() { return stored(SEEN_KEY, ''); }
function isNew(e, since) { return e && !e.mine && (!since || Date.parse(e.at) > Date.parse(since)); }

/* What others changed since the desk last looked, for the rail. */
function othersCount(changed, since) {
  if (!changed) return 0;
  return changed.leaves.filter(l => isNew(l.head_edited, since) || isNew(l.notes_edited, since)).length +
    changed.entities.filter(e => isNew(e.head_edited, since) || isNew(e.notes_edited, since)).length;
}

async function refreshHeadsBadge() {
  const badge = $('#headsCount');
  if (!badge) return;
  try {
    const changed = await api('/api/reports/leaves?view=changed');
    const n = othersCount(changed, seenAt());
    badge.textContent = String(n);
    badge.hidden = !n;
    badge.setAttribute('aria-label', `${n} change${n === 1 ? '' : 's'} by others to review`);
  } catch (err) { /* the rail goes without a count */ }
}

async function loadReview() {
  const P = ReviewPage;
  P.changed = await api('/api/reports/leaves?view=changed');
  if (!P.view) P.view = othersCount(P.changed, seenAt()) ? 'changed' : 'uncategorised';
  if (P.view === 'changed') { P.data = P.changed; return; }
  const q = new URLSearchParams();
  if (P.fy != null) q.set('fy', P.fy);
  if (P.quarter) q.set('quarter', P.quarter);
  P.data = await api(`/api/reports/leaves?${q}`);
  P.fy = P.data.fy;
}

Places.leaves = {
  title: 'Review categories',
  async render(root, params) {
    const P = ReviewPage;
    if (params.view) P.view = params.view;
    if (params.fy) P.fy = Number(params.fy);
    if (params.q !== undefined) P.quarter = Number(params.q) || null;
    root.appendChild(h('p.empty', 'Gathering the cheques…'));
    try { await loadReview(); } catch (err) { clear(root, h('p.empty', err.message)); return; }
    if (!root.isConnected) return;
    root.replaceChildren();
    paintReview(root);
    hydrateIcons(root);
    // Looking at the changes counts as having seen them, for the rail; this
    // visit still marks them new.
    if (P.view === 'changed') {
      P.seenBefore = seenAt();
      store(SEEN_KEY, new Date().toISOString());
      refreshHeadsBadge();
    }
  },
  leave() { ReviewPage.picked.clear(); },
  refresh() {},
};

function reviewCtx() {
  const d = ReviewPage.data;
  return { heads: d.heads, leaves: d.leaves, entities: d.entities, label: ReviewPage.view === 'changed' ? '' : d.label,
    onSaved: () => { route(); refreshHeadsBadge(); } };
}

function reviewRows() {
  const P = ReviewPage, d = P.data;
  if (P.view === 'uncategorised') return d.leaves.filter(l => l.head === 'Uncategorised');
  if (P.view === 'own') return d.leaves.filter(l => l.own_head);
  return d.leaves;
}

function paintReview(root) {
  const P = ReviewPage, d = P.data;
  const pending = othersCount(P.changed, seenAt());
  const views = seg('Show', [
    ['changed', pending ? `Changed by others (${pending})` : 'Changed by others'],
    ['uncategorised', 'No category'], ['own', 'Own category'], ['all', 'All']], P.view,
    v => go('leaves', { view: v }));
  let period = null;
  if (P.view !== 'changed') {
    const fySel = h('select', { 'aria-label': 'Financial year' }, d.years.map(y => h('option', { value: String(y.fy), selected: y.fy === d.fy }, y.label)));
    fySel.addEventListener('change', () => go('leaves', { view: P.view, fy: fySel.value, q: '0' }));
    period = h('div.ann-period',
      seg('Period', [['', 'FY'], ['1', 'Q1'], ['2', 'Q2'], ['3', 'Q3'], ['4', 'Q4']], P.quarter ? String(P.quarter) : '',
        v => go('leaves', { view: P.view, fy: String(d.fy), q: v || '0' })),
      fySel);
  }
  const rows = reviewRows();
  const changedEntities = d.entities.filter(e => (e.head_edited && !e.head_edited.mine) || (e.notes_edited && !e.notes_edited.mine));
  const headline = {
    changed: rows.length || changedEntities.length
      ? `${plural(rows.length + changedEntities.length, 'change')} by members, newest first.`
      : 'Nobody else has changed a category or a note.',
    uncategorised: rows.length ? `${plural(rows.length, 'cheque')} with no category in ${d.label}.` : `Every cheque in ${d.label} has a category.`,
    own: rows.length ? `${plural(rows.length, 'cheque')} with a category of their own in ${d.label}.` : `No cheque in ${d.label} has a category of its own.`,
    all: `${plural(rows.length, 'cheque')} in ${d.label}.`,
  }[P.view];

  appendTo(root,
    h('div.page-head.ann-review-head',
      h('div',
        h('h2.headline', headline),
        h('p.lede', 'Members change categories and notes from the Year and Monthly reports; the latest change counts. Check theirs here, give a category to what has none, or select several cheques to change their category together.')),
      h('div.tools', views, period)),
    P.view === 'changed' && changedEntities.length ? entityChanges(changedEntities) : null,
    reviewList(rows));
}

function changedCell(x) {
  const since = ReviewPage.seenBefore;
  const latest = [x.head_edited, x.notes_edited].filter(Boolean).sort((a, b) => (a.at < b.at ? 1 : -1))[0];
  if (!latest) return h('span');
  const fresh = isNew(latest, since);
  return h('span.sub.ann-by',
    fresh ? h('span.ann-new', 'New') : null,
    `${latest.mine ? 'This desk' : latest.by} · ${fmtStamp(latest.at)}`);
}

function entityChanges(changed) {
  const ctx = reviewCtx();
  return h('section.panel',
    h('div.panel-head', h('h3', 'Entities')),
    h('ul.ann-review', changed.map(e => h('li.ann-review-item',
      h('span'),
      h('button.ann-review-row', { type: 'button', onclick: () => Annotate.entity(e, ctx),
          'aria-label': `${e.name}: category ${e.head}. Change category or note` },
        h('span.ann-review-when', h('span.sub', 'Entity')),
        h('span.ann-review-name', h('span.strong', e.name), e.payee_name !== e.name ? h('span.sub', `pays ${e.payee_name}`) : null),
        h('span'),
        h('span.ann-inherit', e.head),
        h('span.ann-review-note', e.notes || ''),
        changedCell(e))))));
}

function reviewList(rows) {
  const P = ReviewPage, d = P.data, ctx = reviewCtx();
  const visible = new Set(rows.map(l => l.id));
  [...P.picked].forEach(id => { if (!visible.has(id)) P.picked.delete(id); });
  const repaint = focusId => {
    $('.ann-review-panel').replaceWith(reviewList(rows));
    hydrateIcons($('.ann-review-panel'));
    const again = focusId != null && $(`.ann-review-panel input[data-id="${focusId}"]`);
    if (again) again.focus();
  };

  const bulk = P.picked.size ? h('div.ann-bulk', { role: 'region', 'aria-label': 'Selected cheques' },
    h('span.strong', `${plural(P.picked.size, 'cheque')} selected`),
    h('span.sub', 'Set category to'),
    h('div.ann-bulk-heads', [['', 'Same as entity'], ...d.heads.map(x => [x, x])].map(([value, text]) =>
      h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => bulkHead(value, rows) },
        value ? h('i.ann-dot', { style: { background: annotateColour(ctx, value) } }) : null, text))),
    h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => { P.picked.clear(); repaint(); } }, 'Clear selection')) : null;

  const list = rows.length ? h('ul.ann-review', rows.map(l => h('li.ann-review-item' + (P.picked.has(l.id) ? '.is-picked' : ''),
    h('input', { type: 'checkbox', checked: P.picked.has(l.id), dataset: { id: String(l.id) }, 'aria-label': `Select cheque ${l.cheque_no || l.id}`,
      onchange: ev => { if (ev.target.checked) P.picked.add(l.id); else P.picked.delete(l.id); repaint(l.id); } }),
    h('button.ann-review-row', { type: 'button', onclick: () => Annotate.leaf(l, ctx),
        'aria-label': `Cheque ${l.cheque_no || ''}, ${l.entity_name}, ${rupees(l.amount_paise, { always: true })}, category ${l.head}. Change category or note` },
      h('span.ann-review-when', h('span.mono', l.cheque_no || '—'), h('span.sub', fmtDay(l.cheque_date))),
      h('span.ann-review-name', h('span.strong', l.entity_name), l.payee !== l.entity_name ? h('span.sub', l.payee) : null),
      h('span.mono.num', rupees(l.amount_paise, { always: true })),
      headTag(l, ctx),
      h('span.ann-review-note', l.notes || ''),
      changedCell(l)))))
    : h('p.empty', P.view === 'changed' ? 'When a member changes a category or a note, it appears here.' : 'Nothing to show for this period.');

  return h('section.panel.ann-review-panel', bulk, list);
}

/* Several cheques to one head at once, with one Undo for all of them. */
async function bulkHead(value, rows) {
  const P = ReviewPage;
  const picked = rows.filter(l => P.picked.has(l.id));
  const before = new Map(picked.map(l => [l.id, l.own_head || '']));
  const done = [];
  try {
    for (const l of picked) {
      if ((l.own_head || '') === value) continue;
      await post('/api/annotations', { subject: `cheque:${l.id}`, field: 'head', value });
      done.push(l.id);
    }
  } catch (err) {
    toast(`Stopped after ${plural(done.length, 'cheque')}: ${err.message}`, true);
    route();
    return;
  }
  P.picked.clear();
  route();
  refreshHeadsBadge();
  if (!done.length) { toast('Those cheques already had that category.'); return; }
  toastUndo(`${plural(done.length, 'cheque')} moved to another category.`, async () => {
    try {
      for (const id of done) await post('/api/annotations', { subject: `cheque:${id}`, field: 'head', value: before.get(id) });
      toast('Put back as it was.');
      route();
    } catch (err) { toast(`Could not undo: ${err.message}`, true); }
  });
}

// The rail's count, on the desk only, kept fresh while the desk is open.
if ($('#headsCount')) {
  refreshHeadsBadge();
  setInterval(() => { if (!document.hidden) refreshHeadsBadge(); }, 120000);
}
