/* Monthly: each month's spend under the expense heads.
 *
 * Two ranges: one Indian financial year (April to March, every month listed
 * so a nil month reads as nil) or the last six months, which may straddle two
 * years. Every sum is the server's, in paise (/api/reports/monthly). Heads keep
 * a fixed colour each, so a head reads the same in either range.
 */
'use strict';

const MonthlyPage = { range: stored('banyan.monthly.range', 'fy'), fy: null, data: null, leaves: null, open: new Set() };

function monthlyColour(heads) {
  const map = new Map(heads.map((x, i) => [x.head, HEAD_COLOURS[i] || OTHER_COLOUR]));
  return name => map.get(name) || OTHER_COLOUR;
}

Places.monthly = {
  title: 'Monthly',
  async render(root, params) {
    const P = MonthlyPage;
    if (params.range === 'fy' || params.range === '6') { P.range = params.range; store('banyan.monthly.range', P.range); }
    if (params.fy) P.fy = Number(params.fy);
    root.appendChild(h('p.empty', 'Adding it up…'));
    const q = new URLSearchParams();
    if (P.range === '6') q.set('last', '6');
    else if (P.fy != null) q.set('fy', P.fy);
    try { P.data = await api(`/api/reports/monthly?${q}`); }
    catch (err) { clear(root, h('p.empty', err.message)); return; }
    // The months' cheques with their heads and notes, for opening an entity's line.
    P.leaves = null;
    const months = P.data.months;
    if (months.length) {
      const lq = new URLSearchParams({ from: `${months[0].month}-01`, to: `${months[months.length - 1].month}-31` });
      try { P.leaves = await api(`/api/reports/leaves?${lq}`); } catch (err) { P.leaves = null; }
    }
    if (!root.isConnected) return;
    if (P.data.fy != null) P.fy = P.data.fy;
    root.replaceChildren();
    paintMonthly(root, P.data);
    hydrateIcons(root);
  },
  refresh() {},
};

function paintMonthly(root, d) {
  const P = MonthlyPage;
  const colour = monthlyColour(d.heads);
  const t = d.totals;
  const range = seg('Range', [['fy', 'Financial year'], ['6', 'Last 6 months']], P.range,
    v => go('monthly', { range: v }));
  const fySel = h('select', { 'aria-label': 'Financial year', hidden: P.range !== 'fy' },
    d.years.map(y => h('option', { value: String(y.fy), selected: y.fy === P.fy }, y.label)));
  fySel.addEventListener('change', () => go('monthly', { range: 'fy', fy: fySel.value }));
  const pdfQ = P.range === '6' ? 'last=6' : `fy=${P.fy}`;
  const printBtn = h('a.btn.btn-primary', { href: `/api/reports/monthly.pdf?${pdfQ}`, download: '' }, 'Download PDF');

  // Months not yet begun, and months before the register's first cheque,
  // would only be rows of dashes.
  const begun = d.months.filter(m => !m.future);
  const first = begun.findIndex(m => m.count);
  const shown = first < 0 ? begun : begun.slice(first);
  const headline = t.count
    ? `${lakhs(t.paise, true)} paid ${d.mode === 'fy' ? 'in ' + d.label : 'in the last six months'}.`
    : `Nothing paid ${d.mode === 'fy' ? 'in ' + d.label : 'in the last six months'}.`;
  const lede = t.count
    ? `${lakhs(t.per_month_paise, true)} a month on average, over the ${t.months_paid} month${t.months_paid === 1 ? '' : 's'} with cheques.`
    : '';
  const span = shown.length ? `${shown[0].label} to ${shown[shown.length - 1].label}` : d.label;

  appendTo(root,
    h('div.page-head',
      h('div',
        h('div.eyebrow', d.mode === 'fy' ? `${d.label}, April to March` : span),
        h('h2.headline', headline),
        lede ? h('p.lede', lede) : null),
      h('div.tools.no-print', range, fySel, printBtn)),
    t.count ? monthlyMatrix(d, shown, colour) : null,
    t.count ? monthlyBars(d, shown, colour) : null,
    t.count ? monthlyDetail(d, shown, colour) : null);
}

function monthlyMoney(p) { return p ? rupees(p, { whole: true }) : '—'; }

/* Heads down, months across, totals both ways. */
function monthlyMatrix(d, months, colour) {
  return h('section.panel.mo-panel',
    h('div.panel-head', h('h3', 'Month by category')),
    h('div.mo-scroll',
      h('table.head-table.mo-table',
        h('thead', h('tr', h('th', 'Category'),
          months.map(m => h('th.num', m.label.replace(/ (\d\d)(\d\d)$/, ' $2'), m.current ? h('span.mo-part', 'to date') : null)),
          h('th.num', 'Total'))),
        h('tbody', d.heads.map(x => h('tr',
          h('td', h('i.mo-sw', { style: { background: colour(x.head) } }), x.head),
          months.map(m => h('td.mono.num' + (m.heads[x.head] ? '' : '.mo-nil'), monthlyMoney(m.heads[x.head]))),
          h('td.mono.num.strong', monthlyMoney(x.paise))))),
        h('tfoot', h('tr', h('th', 'Total'), months.map(m => h('th.mono.num', monthlyMoney(m.paise))),
          h('th.mono.num', monthlyMoney(d.totals.paise)))))));
}

/* One bar per month, split by head, all on the same scale. */
function monthlyBars(d, months, colour) {
  const max = Math.max(...months.map(m => m.paise), 1);
  return h('section.panel.mo-panel',
    h('div.panel-head', h('h3', 'How each month splits'),
      h('div.mo-legend', d.heads.filter(x => x.paise).map(x =>
        h('span', h('i.mo-sw', { style: { background: colour(x.head) } }), x.head)))),
    months.map(m => h('div.mo-bar-row',
      h('span.mo-bl', m.label),
      h('div.mo-bar', d.heads.filter(x => m.heads[x.head]).map(x =>
        h('span', { title: `${x.head} ${rupees(m.heads[x.head], { whole: true })}`,
          style: { background: colour(x.head), flexBasis: `${(m.heads[x.head] / max * 100).toFixed(2)}%` } }))),
      h('span.mono.num', monthlyMoney(m.paise)))));
}

/* Newest month first: each head and the entities paid under it. An entity's
   line opens to its cheques that month, where a head or note can be changed. */
function monthlyDetail(d, months, colour) {
  const P = MonthlyPage, L = P.leaves;
  const repaint = key => {
    $('.mo-detail').replaceWith(monthlyDetail(d, months, colour));
    hydrateIcons($('.mo-detail'));
    const again = $(`[data-drill="${CSS.escape(key)}"]`);
    if (again) again.focus();
  };
  const entityLine = (m, e) => {
    const plain = h('li', h('span', e.name), h('span.mono.num', monthlyMoney(e.paise)));
    if (!L) return plain;
    const leaves = L.leaves.filter(l => l.cheque_date.slice(0, 7) === m.month && l.head === e.head && l.entity_name === e.name);
    if (!leaves.length) return plain;
    const ent = L.entities.find(x => x.id === leaves[0].entity_id);
    const noted = (ent && ent.notes) || (leaves.find(l => l.notes) || {}).notes;
    const key = `${m.month}|${e.head}|${e.name}`;
    const open = P.open.has(key);
    const id = 'mo-drill-' + key.replace(/[^a-z0-9]+/gi, '-');
    const ctx = { heads: L.heads, colour, leaves: L.leaves, entities: L.entities, label: m.label, onSaved: () => route() };
    const toggle = () => { if (P.open.has(key)) P.open.delete(key); else P.open.add(key); repaint(key); };
    return [
      h('li.ann-mo-item' + (open ? '.is-open' : ''),
        h('button.ann-toggle', { type: 'button', 'aria-expanded': String(open), 'aria-controls': id, 'data-drill': key, onclick: toggle },
          e.name), noteMark(noted),
        h('span.mono.num', monthlyMoney(e.paise))),
      open ? h('li.ann-mo-drill', { id }, leafDrill(leaves, ctx)) : null];
  };
  return h('section.panel.mo-panel.mo-detail',
    h('div.panel-head', h('h3', 'Month by month')),
    months.filter(m => m.count).reverse().map(m => h('div.mo-month',
      h('div.mo-month-head',
        h('h4', m.label, m.current ? h('span.mo-part', 'month to date') : null),
        h('p', h('span.mono', monthlyMoney(m.paise)), h('span.sub', ` across ${m.count} cheque${m.count === 1 ? '' : 's'}`))),
      h('div.mo-groups', d.heads.filter(x => m.heads[x.head]).map(x => h('div.mo-group',
        h('div.mo-group-head', h('i.mo-sw', { style: { background: colour(x.head) } }), h('span', x.head),
          h('span.mono.num', monthlyMoney(m.heads[x.head]))),
        h('ul', m.entities.filter(e => e.head === x.head).map(e => entityLine(m, e)))))))));
}
