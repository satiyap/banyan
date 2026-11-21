/* Heads & notes: the report head and the note on each entity and each leaf.
 *
 * Anyone with a key may change these two things, here, from the desk, the
 * members' pages or a reports-only app. The last change to reach the server
 * wins. Nothing else about a cheque can be changed here: the amount, the
 * leaf, its status and what was printed belong to the desk.
 */
'use strict';

const LeavesPage = { fy: null, quarter: null, tab: 'leaves', head: '', q: '', data: null };

async function loadLeaves() {
  const P = LeavesPage;
  const q = new URLSearchParams();
  if (P.fy != null) q.set('fy', P.fy);
  if (P.quarter) q.set('quarter', P.quarter);
  P.data = await api(`/api/reports/leaves?${q}`);
  P.fy = P.data.fy;
}

Places.leaves = {
  title: 'Heads & notes',
  async render(root, params) {
    const P = LeavesPage;
    if (params.fy) P.fy = Number(params.fy);
    if (params.q !== undefined) P.quarter = Number(params.q) || null;
    root.appendChild(h('p.empty', 'Fetching the leaves…'));
    try { await loadLeaves(); } catch (err) { clear(root, h('p.empty', err.message)); return; }
    if (!root.isConnected) return;
    root.replaceChildren();
    paintLeaves(root);
    hydrateIcons(root);
  },
  refresh() {},
};

/* Save one field, then redraw with what the server now says. */
async function saveAnnotation(subject, field, value, control) {
  control.disabled = true;
  try {
    await post('/api/annotations', { subject, field, value });
    toast(field === 'head' ? 'Head saved.' : 'Note saved.');
    const content = $('#content');
    const keep = content.scrollTop;
    await loadLeaves();
    content.replaceChildren();
    paintLeaves(content);
    hydrateIcons(content);
    content.scrollTop = keep;
  } catch (err) {
    toast(err.message, true);
    control.disabled = false;
  }
}

function editedLine(head, notes) {
  const latest = [head, notes].filter(Boolean).sort((a, b) => (a.at < b.at ? 1 : -1))[0];
  return latest ? h('div.sub.ann-edited', `Changed by ${latest.by || 'someone'} · ${fmtStamp(latest.at)}`) : null;
}

function headSelect(label, value, options, onPick) {
  const sel = h('select', { 'aria-label': label },
    options.map(([v, text]) => h('option', { value: v, selected: v === value }, text)));
  sel.addEventListener('change', () => onPick(sel.value, sel));
  return sel;
}

function noteInput(label, value, onSave) {
  const input = h('input.ann-note', { type: 'text', value, maxLength: 500, placeholder: 'Add a note', 'aria-label': label });
  const commit = () => { if (input.value.trim() !== value) onSave(input.value, input); };
  input.addEventListener('change', commit);
  input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); input.blur(); } });
  return input;
}

function matches(text) {
  const q = LeavesPage.q.trim().toLowerCase();
  return !q || text.toLowerCase().includes(q);
}

function paintLeaves(root) {
  const P = LeavesPage, d = P.data;
  const fySel = h('select', { 'aria-label': 'Financial year' }, d.years.map(y => h('option', { value: String(y.fy), selected: y.fy === d.fy }, y.label)));
  fySel.addEventListener('change', () => go('leaves', { fy: fySel.value, q: '0' }));
  const period = seg('Period', [['', 'FY'], ['1', 'Q1'], ['2', 'Q2'], ['3', 'Q3'], ['4', 'Q4']], P.quarter ? String(P.quarter) : '',
    v => go('leaves', { fy: String(d.fy), q: v || '0' }));
  const tabs = seg('Show', [['leaves', 'Leaves'], ['entities', 'Entities']], P.tab, v => { P.tab = v; repaint(); });
  const headFilter = headSelect('Head', P.head,
    [['', 'All heads'], ...d.heads.map(x => [x, x]), ['Uncategorised', 'Uncategorised']],
    v => { P.head = v; repaint(); });
  const search = h('input.ann-search', { type: 'search', value: P.q, placeholder: 'Search name, payee, leaf or note', 'aria-label': 'Search' });
  search.addEventListener('input', () => { P.q = search.value; const list = $('.ann-list'); if (list) list.replaceWith(listPanel()); });

  function repaint() { clear(root); paintLeaves(root); hydrateIcons(root); }

  appendTo(root,
    h('div.page-head',
      h('div',
        h('div.eyebrow', `${d.label} · ${plural(d.leaves.length, 'leaf', 'leaves')}`),
        h('h2.headline', 'Heads and notes'),
        h('p.lede', 'Anyone with a key can change the report head and the note, on an entity or on one leaf. The latest change wins, and each one is recorded with who made it. Amounts, leaves and what was printed stay with the desk.')),
      h('div.tools', period, fySel)),
    h('section.panel',
      h('div.panel-head', tabs, headFilter, search),
      listPanel()));

  function listPanel() {
    return P.tab === 'entities' ? entityList() : leafList();
  }

  function leafList() {
    const rows = d.leaves
      .filter(l => !P.head || l.head === P.head)
      .filter(l => matches([l.entity_name, l.payee, l.cheque_no, l.notes].join(' ')))
      .map(l => {
        const subject = `cheque:${l.id}`;
        const options = [['', `Entity's head (${l.entity_head})`], ...d.heads.map(x => [x, x])];
        return h('div.ann-row',
          h('div', h('div.strong', l.entity_name), h('div.sub', l.payee !== l.entity_name ? l.payee : '')),
          h('div', h('div.mono', l.cheque_no || '—'), h('div.sub', fmtDay(l.cheque_date))),
          h('div.mono.num', rupees(l.amount_paise, { always: true })),
          h('div', headSelect(`Head for leaf ${l.cheque_no || l.id}`, l.own_head, options,
            (v, c) => saveAnnotation(subject, 'head', v, c))),
          h('div', noteInput(`Note for leaf ${l.cheque_no || l.id}`, l.notes,
            (v, c) => saveAnnotation(subject, 'notes', v, c)),
            editedLine(l.head_edited, l.notes_edited)));
      });
    return h('div.ann-list',
      h('div.ann-row.ann-headings', h('div', 'Paid to'), h('div', 'Leaf'), h('div.num', 'Amount'), h('div', 'Head'), h('div', 'Note')),
      rows.length ? rows : h('p.empty', 'No leaves match.'));
  }

  function entityList() {
    const rows = d.entities
      .filter(e => !P.head || e.head === P.head)
      .filter(e => matches([e.name, e.payee_name, e.notes].join(' ')))
      .map(e => {
        const subject = `entity:${e.id}`;
        const none = e.kind === 'one_off' ? 'None chosen (Misc)' : 'None chosen (Uncategorised)';
        return h('div.ann-row.is-entity',
          h('div', h('div.strong', e.name), h('div.sub', e.payee_name !== e.name ? `pays ${e.payee_name}` : '')),
          h('div.sub', plural(e.count, 'leaf', 'leaves')),
          h('div.mono.num', rupees(e.paise, { whole: true })),
          h('div', headSelect(`Head for ${e.name}`, e.head_set, [['', none], ...d.heads.map(x => [x, x])],
            (v, c) => saveAnnotation(subject, 'head', v, c))),
          h('div', noteInput(`Note for ${e.name}`, e.notes, (v, c) => saveAnnotation(subject, 'notes', v, c)),
            editedLine(e.head_edited, e.notes_edited)));
      });
    return h('div.ann-list',
      h('div.ann-row.ann-headings', h('div', 'Entity'), h('div', 'Leaves'), h('div.num', 'Paid'), h('div', 'Head'), h('div', 'Note')),
      rows.length ? rows : h('p.empty', 'No entities match.'),
      h('p.foot', 'An entity\'s head applies to all its leaves, except a leaf given a head of its own.'));
  }
}
