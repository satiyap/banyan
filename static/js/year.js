/* The Year: where the money went.
 *
 * Financial years run April to March (Q1 = Apr–Jun). Every sum is the
 * server's, in paise (/api/reports); this only lays it out. Expense heads keep
 * one colour for the whole year — taken from the year's ranking, not the
 * period shown — so switching quarter never repaints a head.
 */
'use strict';

const YearPage = { fy: null, quarter: null, tab: 'entities', fyData: null, data: null, leaves: null, quarters: null, open: new Set() };

/* Categorical slots in their validated order; heads past the seventh fold
   into Other, which is always grey. */
const HEAD_COLOURS = ['#34548a', '#2f7d74', '#a07a1c', '#7a4e8c', '#a8503a', '#4f7a4a', '#5f7896'];  // muted inks for Studio's cool paper; orange stays the action colour
const OTHER_COLOUR = '#9aa3a8';

function headPalette(fyData) {
  const map = new Map();
  fyData.categories.slice(0, HEAD_COLOURS.length).forEach((c, i) => map.set(c.category, HEAD_COLOURS[i]));
  return name => map.get(name) || OTHER_COLOUR;
}

async function loadYear() {
  const P = YearPage;
  const q = new URLSearchParams();
  if (P.fy != null) q.set('fy', P.fy);
  P.fyData = await api(`/api/reports?${q}`);
  P.fy = P.fyData.fy;
  if (P.quarter) {
    q.set('fy', P.fy);
    q.set('quarter', P.quarter);
    P.data = await api(`/api/reports?${q}`);
  } else P.data = P.fyData;
  // The period's cheques with their heads and notes, for opening a row of
  // the ledger. A server without them still shows the report.
  const lq = new URLSearchParams({ fy: String(P.fy) });
  if (P.quarter) lq.set('quarter', P.quarter);
  try { P.leaves = await api(`/api/reports/leaves?${lq}`); } catch (err) { P.leaves = null; }
  // Quarterly income and balance; a server without it still shows the report.
  try { P.quarters = await api('/api/reports/quarters'); } catch (err) { P.quarters = null; }
}

/* What the edit panel needs from this report. */
function yearCtx(d, colour) {
  const L = YearPage.leaves;
  return { heads: L.heads, colour, leaves: L.leaves, entities: L.entities, label: d.label.split(' (')[0],
    onSaved: () => route() };
}

/* A ledger row that opens to the cheques behind it. The name is the
   control; a click anywhere on the row does the same. */
function drillRow(key, cls, cells, body, d, colour) {
  const P = YearPage;
  const open = P.open.has(key);
  const id = 'drill-' + key.replace(/[^a-z0-9]+/gi, '-');
  const toggle = () => {
    if (P.open.has(key)) P.open.delete(key); else P.open.add(key);
    const panel = $('.ledger');
    panel.replaceWith(ledgerPanel(d, colour));
    hydrateIcons($('.ledger'));
    const again = $(`[data-drill="${CSS.escape(key)}"]`);
    if (again) again.focus();
  };
  const row = h('div.lrow.ann-row-toggle' + cls + (open ? '.is-open' : ''), cells(
    h('button.ann-toggle', { type: 'button', 'aria-expanded': String(open), 'aria-controls': id, 'data-drill': key,
      onclick: ev => { ev.stopPropagation(); toggle(); } })));
  row.addEventListener('click', ev => { if (!ev.target.closest('a, button')) toggle(); });
  return [row, open ? h('div.ann-drill-wrap', { id }, body()) : null];
}

Places.year = {
  title: 'Year',
  async render(root, params) {
    const P = YearPage;
    if (params.fy) P.fy = Number(params.fy);
    if (params.q !== undefined) P.quarter = Number(params.q) || null;
    root.appendChild(h('p.empty', 'Adding it up…'));
    try { await loadYear(); } catch (err) { clear(root, h('p.empty', err.message)); return; }
    if (!root.isConnected) return;
    root.replaceChildren();
    paintYear(root);
    hydrateIcons(root);
  },
  refresh() {},
};

function yearHeadline(fd, d) {
  const t = d.totals;
  const monthsWord = n => {
    const words = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten', 'eleven', 'twelve'];
    return `${words[n] || n} month${n === 1 ? '' : 's'}`;
  };
  if (!t.count) return [`Nothing paid in ${d.label.split(' (')[0]}${t.in_progress ? ' yet' : ''}.`, ''];
  const when = d.quarter ? `in ${d.label.split(' (')[0]}` : t.in_progress ? `in ${monthsWord(t.months_elapsed)}` : `in ${d.label}`;
  const head = `${lakhs(t.paise, true)} paid ${when}${t.in_progress && d.quarter ? ' so far' : ''}.`;
  const bits = [];
  if (t.per_month_paise) bits.push(`${lakhs(t.per_month_paise, true)} a month on average`);
  const prevAvg = t.previous.per_month_paise;
  if (prevAvg && t.per_month_paise) {
    const pct = Math.round((t.per_month_paise - prevAvg) / prevAvg * 100);
    bits[0] += pct === 0 ? `, the same as ${t.previous.label}’s` : `, which is ${Math.abs(pct)}% ${pct > 0 ? 'more' : 'less'} than ${t.previous.label}’s`;
  }
  let sentence = bits.length ? bits.join('') + '.' : '';
  const cats = d.categories;
  if (cats.length >= 2 && cats[0].share + cats[1].share >= 0.4) {
    sentence += ` ${cats[0].category} and ${cats[1].category} are ${Math.round((cats[0].share + cats[1].share) * 100)}% of everything.`;
  } else if (cats.length && cats[0].share >= 0.3) {
    sentence += ` ${cats[0].category} is ${Math.round(cats[0].share * 100)}% of everything.`;
  }
  return [head, sentence.trim()];
}

function paintYear(root) {
  const P = YearPage;
  const fd = P.fyData, d = P.data;
  const colour = headPalette(fd);
  const cur = fd.quarters.find(q => q.state === 'current');
  const [headline, lede] = yearHeadline(fd, d);
  const fySel = h('select', { 'aria-label': 'Financial year' }, fd.years.map(y => h('option', { value: String(y.fy), selected: y.fy === fd.fy }, y.label)));
  fySel.addEventListener('change', () => go('year', { fy: fySel.value, q: '0' }));
  const period = seg('Period', [['', 'FY'], ['1', 'Q1'], ['2', 'Q2'], ['3', 'Q3'], ['4', 'Q4']], P.quarter ? String(P.quarter) : '',
    v => go('year', { fy: String(fd.fy), q: v || '0' }));

  appendTo(root, 
    h('div.page-head',
      h('div',
        h('div.eyebrow', `${fd.label} · April to March${cur ? ` · Q${cur.quarter} in progress` : ''}`),
        h('h2.headline', headline),
        h('p.lede', lede)),
      h('div.tools', period, fySel)),
    headNote(d),
    h('div.year-top' + (dominantHead(d) ? '.is-single' : ''),
      dominantHead(d) ? null : ringPanel(d, colour),
      monthsPanel(fd, colour)),
    incomePanel(P.quarters),
    ledgerPanel(d, colour));
}

/* When one head holds nearly everything, a ring says nothing: say it in a
   line instead and give the months the room. */
function dominantHead(d) {
  const top = d.categories.reduce((m, c) => (!m || c.share > m.share ? c : m), null);
  return d.totals.count && top && top.share >= 0.9 ? top : null;
}
function headNote(d) {
  const top = dominantHead(d);
  if (!top) return null;
  return h('div.note-line', icon('book'),
    `${Math.round(top.share * 100)}% of this period is ${top.category}. `,
    top.category === 'Uncategorised' ? 'Give entities a category: open one in the ledger below and choose Change category or note, and the year splits by category here.' : 'The split by category returns once spending spreads across categories.');
}

/* ------------------------------------------------------------- ring */
function ringPanel(d, colour) {
  const cats = d.categories;
  const shown = [], other = { category: 'Other', paise: 0, share: 0 };
  cats.forEach(c => { if (colour(c.category) === OTHER_COLOUR) { other.paise += c.paise; other.share += c.share; } else shown.push(c); });
  if (other.paise) shown.push(other);
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 200 200');
  svg.setAttribute('class', 'ring');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', `Spending by category: ${shown.map(c => `${c.category} ${Math.round(c.share * 100)}%`).join(', ')}`);
  const R = 80, C = 2 * Math.PI * R;
  let at = 0;
  const total = d.totals.paise || 1;
  shown.forEach(c => {
    const frac = c.paise / total;
    const seg2 = document.createElementNS(ns, 'circle');
    seg2.setAttribute('cx', '100'); seg2.setAttribute('cy', '100'); seg2.setAttribute('r', String(R));
    seg2.setAttribute('fill', 'none');
    seg2.setAttribute('stroke', c.category === 'Other' ? OTHER_COLOUR : colour(c.category));
    seg2.setAttribute('stroke-width', '30');
    // a 2-unit surface gap between segments
    const len = Math.max(0, frac * C - (shown.length > 1 ? 2 : 0));
    seg2.setAttribute('stroke-dasharray', `${len} ${C - len}`);
    seg2.setAttribute('stroke-dashoffset', String(-at * C));
    seg2.setAttribute('transform', 'rotate(-90 100 100)');
    const title = document.createElementNS(ns, 'title');
    title.textContent = `${c.category}: ${rupees(c.paise, { whole: true })} (${(c.share * 100).toFixed(1)}%)`;
    seg2.appendChild(title);
    svg.appendChild(seg2);
    at += frac;
  });
  return h('section.panel.ring-panel',
    h('div.eyebrow', 'By category'),
    h('div.ring-wrap',
      h('div.ring-box', svg, h('div.ring-center', h('div.ring-total', lakhs(d.totals.paise)), h('div.sub', plural(d.totals.count, 'cheque')))),
      h('ul.ring-legend', shown.map(c => h('li', h('i', { style: { background: c.category === 'Other' ? OTHER_COLOUR : colour(c.category) } }),
        h('span', c.category), h('span.mono', `${Math.round(c.share * 100)}%`))))),
    !shown.length ? h('p.empty', 'No cheques in this period.') : null);
}

/* ------------------------------------------------------------- months */
function monthsPanel(fd, colour) {
  const max = Math.max(1, ...fd.months.map(m => m.paise));
  const tip = h('div.viz-tip', { hidden: true, role: 'tooltip' });
  const bands = fd.quarters.map(q => {
    const months = fd.months.filter(m => m.quarter === q.quarter);
    const sel = YearPage.quarter === q.quarter;
    return h('a.qband.state-' + q.state + (sel ? '.is-selected' : ''), { href: `#/year?fy=${fd.fy}&q=${YearPage.quarter === q.quarter ? 0 : q.quarter}`,
        'aria-label': `${q.name}: ${rupees(q.paise, { whole: true })}` },
      h('div.qband-head', h('span.strong', `Q${q.quarter}${q.state === 'current' ? ' · now' : q.state === 'closed' ? ' · closed' : ''}`),
        h('span.mono', q.state === 'future' && !q.paise ? '—' : `${lakhs(q.paise)}${q.state === 'current' ? ' so far' : ''}`)),
      h('div.qbars', months.map(m => {
        const col = h('div.mbar' + (m.future ? '.is-future' : ''));
        const stack = h('div.mstack', { style: { height: (m.paise / max * 100) + '%' } });
        // Heads past the coloured seven are one Other segment, as in the ring.
        const parts = new Map();
        Object.entries(m.heads || {}).forEach(([head, p]) => {
          const key = colour(head) === OTHER_COLOUR ? 'Other' : head;
          parts.set(key, (parts.get(key) || 0) + p);
        });
        const rank = name => name === 'Other' ? 99 : fd.categories.findIndex(c => c.category === name);
        [...parts].sort((a, b) => rank(a[0]) - rank(b[0]))
          .forEach(([head, p]) => stack.appendChild(h('i', { style: { flexGrow: String(p), background: head === 'Other' ? OTHER_COLOUR : colour(head) } })));
        appendTo(col, h('span.mval.mono', m.paise ? (m.paise / 1e7).toFixed(1) : ''), h('div.mtrack', stack), h('span.mlabel', m.label));
        col.addEventListener('mouseenter', ev => {
          if (!m.paise) return;
          clear(tip, h('div.strong', `${m.label} · ${rupees(m.paise, { whole: true })}`), h('div.sub', plural(m.count, 'cheque')),
            Object.entries(m.heads || {}).sort((a, b) => b[1] - a[1]).map(([head, p]) =>
              h('div.tip-row', h('i', { style: { background: colour(head) } }), h('span', head), h('span.mono', rupees(p, { whole: true })))));
          tip.hidden = false;
          const r = col.getBoundingClientRect(), pr = col.closest('.months-panel').getBoundingClientRect();
          Object.assign(tip.style, { left: (r.left - pr.left + r.width / 2) + 'px', top: (r.top - pr.top) + 'px' });
        });
        col.addEventListener('mouseleave', () => { tip.hidden = true; });
        return col;
      })));
  });
  return h('section.panel.months-panel',
    h('div.panel-head', h('span.eyebrow', 'Month by month, grouped by quarter'), h('span.sub', 'hover a month for its categories · ₹ lakh · choose a quarter to report on it')),
    h('div.qbands', bands), tip);
}

/* ------------------------------------------------------------- ledger */
function ledgerPanel(d, colour) {
  const P = YearPage;
  const q = new URLSearchParams({ fy: String(d.fy) });
  if (d.quarter) q.set('quarter', String(d.quarter));
  const csvKind = { entities: 'entities', heads: 'categories' }[P.tab];
  const tabs = seg('Ledger', [['entities', 'Entities'], ['heads', 'Categories']], P.tab,
    v => { P.tab = v; const panel = $('.ledger'); panel.replaceWith(ledgerPanel(d, colour)); hydrateIcons($('.ledger')); });
  const vs = (now, before) => {
    if (!before) return h('span.sub', now ? 'new' : '');
    const pct = Math.round((now - before) / before * 100);
    return h('span.sub' + (pct > 0 ? '.up' : pct < 0 ? '.down' : ''), `${pct > 0 ? '+' : ''}${pct}% vs ${d.totals.previous.label.split(' (')[0]}`);
  };
  // Each bar is its share of the period, beside the same percentage, so the rows add up to one full bar.
  const bar = (share, c) => h('div.lbar', h('i', { style: { width: (share * 100) + '%', background: c } }));
  const L = P.leaves;
  const ctx = L ? yearCtx(d, colour) : null;
  let rows;
  if (P.tab === 'entities') {
    rows = d.entities.map(e => {
      const ent = L && e.entity_id != null ? L.entities.find(x => x.id === e.entity_id) : null;
      const mine = ent ? L.leaves.filter(l => l.entity_id === ent.id) : [];
      const moved = mine.filter(l => l.own_head && l.own_head !== e.category).length;
      const sub = [e.payee_name !== e.entity_name ? `pays ${e.payee_name}` : '', moved ? `${plural(moved, 'cheque')} in another category` : '']
        .filter(Boolean).join(' · ');
      const cells = name => [
        h('div', h('div.strong', name || e.entity_name, ent ? noteMark(ent.notes) : null), h('div.sub', sub)),
        h('div.sub', e.category), h('div.sub', plural(e.count, 'cheque')), h('div.mono', `${(e.share * 100).toFixed(1)}%`),
        bar(e.share, colour(e.category)), h('div.mono.num', rupees(e.paise, { whole: true })), vs(e.paise, e.previous_paise)];
      if (!ent) return h('div.lrow', cells());
      return drillRow('e' + ent.id, '', btn => { btn.textContent = e.entity_name; return cells(btn); }, () => [
        h('div.ann-drill-head',
          h('span', 'Category ', h('strong', ent.head), ent.head_set ? '' : h('span.sub', ' · none chosen')),
          ent.notes ? h('span.ann-drill-note', ent.notes) : null,
          h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => Annotate.entity(ent, ctx) }, 'Change category or note')),
        leafDrill(mine, ctx, true)], d, colour);
    });
  } else {
    rows = d.categories.map(c => {
      const cells = name => [
        h('div.strong', h('i.swatch', { style: { background: colour(c.category) } }), name || c.category),
        h('div.sub', plural(c.entity_count, 'entity', 'entities')), h('div.sub', plural(c.count, 'cheque')), h('div.mono', `${(c.share * 100).toFixed(1)}%`),
        bar(c.share, colour(c.category)), h('div.mono.num', rupees(c.paise, { whole: true })), vs(c.paise, c.previous_paise)];
      if (!L) return h('div.lrow', cells());
      return drillRow('h' + c.category, '', btn => { btn.textContent = c.category; return cells(btn); },
        () => leafDrill(L.leaves.filter(l => l.head === c.category), ctx), d, colour);
    });
  }
  return h('section.panel.ledger',
    h('div.panel-head', h('span.eyebrow', 'Who was paid'), tabs,
      h('a.btn.btn-quiet.btn-xs', { href: `/api/reports/${csvKind}.csv?${q}`, download: '' }, 'CSV')),
    P.tab === 'heads' ? headsTable(d) : null,
    h('div.lrows', rows.length ? rows : h('p.empty', 'Nothing in this period.')),
    h('p.foot', 'Counts every cheque written except void and misprinted ones, by cheque date. One-off entities count under Misc unless another category is chosen.',
      L ? ' Open an entity or a category to see its cheques; anyone with a key can change a category or a note there.' : ''));
}

/* Each head across the quarters (or a quarter's months), with both totals. */
function headsTable(d) {
  const money = p => p ? rupees(p, { whole: true }) : '—';
  return h('table.head-table',
    h('thead', h('tr', h('th', 'Category'), d.columns.map(c => h('th.num', c.label)), h('th.num', 'Total'))),
    h('tbody', d.categories.map(c => h('tr', h('td', c.category),
      c.columns.map(p => h('td.mono.num', money(p))), h('td.mono.num.strong', money(c.paise))))),
    h('tfoot', h('tr', h('th', 'Total'), d.column_totals.map(p => h('th.mono.num', money(p))),
      h('th.mono.num', money(d.totals.paise)))));
}

/* ------------------------------------------------------------- income */
/* Maintenance received each quarter against what was spent, and the balance
   it leaves. Income is typed in at the desk from the bank statements, one
   figure a quarter; spending is the register's cheques by the date they were
   written, plus anything spent outside the register. Members see the totals
   only; the desk alone enters them. */
const INCOME_COLOUR = '#2a6fb0', SPENT_COLOUR = '#a8503a';   // validated as a pair, light surface
const deskCanEnter = () => typeof Places.tray !== 'undefined';

function incomePanel(Q) {
  if (!Q) return null;
  const edit = deskCanEnter();
  const tools = edit ? h('div.btn-row',
    Q.start ? h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => enterQuarter(Q) }, 'Enter a quarter…') : null,
    h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => enterStart(Q) }, Q.start ? 'Starting balance…' : 'Set a starting balance…')) : null;
  const head = h('div.panel-head', h('span.eyebrow', 'Income and spending, quarter by quarter'), tools);
  if (!Q.start) {
    if (!edit) return null;
    return h('section.panel.income-panel', head,
      h('p.sub', 'Start from a quarter whose closing balance you know from the bank statement. Then enter each later quarter’s maintenance, and the balance follows from the cheques written.'));
  }
  const qs = Q.quarters;
  const max = Math.max(1, ...qs.map(q => Math.max(q.income_paise, q.spent_paise)));
  const tip = h('div.viz-tip', { hidden: true, role: 'tooltip' });
  const groups = qs.map(q => {
    const bar = (p, c, what) => h('div.ibar', h('span.mval.mono', p ? (p / 1e7).toFixed(1) : ''),
      h('div.mtrack', h('i.ifill', { style: { height: (p / max * 100) + '%', background: c }, 'aria-hidden': 'true' })), h('span.visually-hidden', what));
    const g = h('div.igroup' + (q.current ? '.is-current' : ''),
      h('div.ipair', bar(q.entered ? q.income_paise : 0, INCOME_COLOUR, 'income'), bar(q.spent_paise, SPENT_COLOUR, 'spent')),
      h('span.mlabel', `Q${q.quarter} ${String(q.fy).slice(2)}–${String(q.fy + 1).slice(2)}${q.current ? ' · now' : ''}`));
    g.addEventListener('mouseenter', () => {
      clear(tip, h('div.strong', q.label + (q.current ? ' · so far' : '')),
        h('div.tip-row', h('i', { style: { background: INCOME_COLOUR } }), h('span', 'Income'), h('span.mono', q.entered ? rupees(q.income_paise, { whole: true }) : 'not entered')),
        h('div.tip-row', h('i', { style: { background: SPENT_COLOUR } }), h('span', 'Spent'), h('span.mono', rupees(q.spent_paise, { whole: true }))),
        h('div.tip-row', h('span'), h('span', 'Closing balance'), h('span.mono', rupees(q.closing_paise, { whole: true }))));
      tip.hidden = false;
      const r = g.getBoundingClientRect(), pr = g.closest('.income-panel').getBoundingClientRect();
      Object.assign(tip.style, { left: (r.left - pr.left + r.width / 2) + 'px', top: (r.top - pr.top) + 'px' });
    });
    g.addEventListener('mouseleave', () => { tip.hidden = true; });
    return g;
  });
  const legend = h('ul.ring-legend.income-legend',
    h('li', h('i', { style: { background: INCOME_COLOUR } }), h('span', 'Maintenance received'), h('span')),
    h('li', h('i', { style: { background: SPENT_COLOUR } }), h('span', 'Spent: cheques written, and anything outside the register'), h('span')));
  const money = p => rupees(p, { whole: true });
  const net = q => h('td.mono.num' + (q.net_paise < 0 ? '.is-over' : ''), (q.net_paise < 0 ? '−' : '+') + money(Math.abs(q.net_paise)));
  const table = h('table.head-table.income-table',
    h('thead', h('tr', h('th', 'Quarter'), h('th.num', 'Income'), h('th.num', 'Spent'), h('th.num', 'Income less spent'), h('th.num', 'Closing balance'))),
    h('tbody',
      h('tr.is-start', h('td', `${Q.start.label} · starting balance`), h('td'), h('td'), h('td'), h('td.mono.num', money(Q.start.closing_paise))),
      qs.map(q => h('tr' + (q.current ? '.is-current' : ''),
        h('td', q.label, q.current ? h('span.sub', ' · so far') : null),
        h('td.mono.num', q.entered ? money(q.income_paise) : h('span.sub', 'not entered')),
        h('td.mono.num', money(q.spent_paise)), net(q), h('td.mono.num', money(q.closing_paise))))));
  return h('section.panel.income-panel', head,
    qs.length ? [h('div.igroups', { role: 'img', 'aria-label': 'Income and spending by quarter; the table below has the figures' }, groups), legend, tip] : null,
    table,
    h('p.foot', 'Income is maintenance received, entered from the bank statements. Spending is the register’s cheques by the date they were written, void and misprinted ones left out, plus anything entered as spent outside the register. The balance runs forward from the starting quarter’s closing balance at the bank. ₹ lakh on the bars.'));
}

/* The quarters that can be entered: after the starting point, up to now. */
async function enterQuarter(Q) {
  const pick = h('select', { 'aria-label': 'Quarter' }, Q.quarters.slice().reverse().map(q =>
    h('option', { value: `${q.fy}-${q.quarter}` }, q.label + (q.entered ? '' : ' · not entered'))));
  const income = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off' });
  const outside = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off' });
  const note = h('p.field-note');
  const fill = () => {
    const q = Q.quarters.find(x => `${x.fy}-${x.quarter}` === pick.value);
    income.value = q && q.entered ? plainAmount(q.income_paise) : '';
    outside.value = q && q.outside_paise ? plainAmount(q.outside_paise) : '';
    note.textContent = q ? `The register has ${rupees(q.register_paise, { whole: true })} of cheques written in ${q.label}.` : '';
  };
  pick.addEventListener('change', fill);
  fill();
  const err = h('p.form-error', { hidden: true });
  await modal({
    eyebrow: 'Income and spending', title: 'Enter a quarter',
    body: h('div', field('Quarter', pick, note),
      field('Maintenance received', income, 'From the bank statement: the quarter’s maintenance credits.'),
      field('Spent outside the register', outside, 'Anything paid in the quarter without a cheque in the register, such as before the register was kept. Usually nothing.'),
      err),
    onOpen: () => income.focus(),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Save', primary: true, value: 'save', onClick: async (ev, dlg) => {
      ev.preventDefault();
      const [fy, quarter] = pick.value.split('-').map(Number);
      try {
        await post('/api/quarters', { fy, quarter, income: income.value.trim(), outside: outside.value.trim() });
        dlg.close('save');
        toast('Saved.');
        route();
      } catch (e) { err.textContent = e.message; err.hidden = false; }
    } }],
  });
}

/* The starting point: one quarter's closing balance at the bank. */
async function enterStart(Q) {
  const cur = Q.current;
  const options = [];
  for (let fy = cur.fy, q = cur.quarter, n = 0; n < 12; n++) {
    options.push([fy, q]);
    if (--q === 0) { q = 4; fy--; }
  }
  // With no starting point yet, last year's Q4: the year's books start from its close.
  const at = Q.start ? `${Q.start.fy}-${Q.start.quarter}` : `${cur.fy - 1}-4`;
  const label = (fy, q) => `Q${q} FY ${fy}–${String(fy + 1).slice(2)}`;
  const pick = h('select', { 'aria-label': 'Starting quarter' }, options.map(([fy, q]) =>
    h('option', { value: `${fy}-${q}`, selected: `${fy}-${q}` === at }, label(fy, q))));
  const closing = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off',
    value: Q.start ? plainAmount(Q.start.closing_paise) : '' });
  const err = h('p.form-error', { hidden: true });
  await modal({
    eyebrow: 'Income and spending', title: 'Starting balance',
    body: h('div', field('Quarter', pick, 'The last quarter before the ones you will enter.'),
      field('Closing balance', closing, 'The bank statement’s balance at the end of that quarter.'), err),
    onOpen: () => closing.focus(),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Save', primary: true, value: 'save', onClick: async (ev, dlg) => {
      ev.preventDefault();
      const [fy, quarter] = pick.value.split('-').map(Number);
      try {
        await post('/api/quarters/start', { fy, quarter, closing: closing.value.trim() });
        dlg.close('save');
        toast('Starting balance saved.');
        route();
      } catch (e) { err.textContent = e.message; err.hidden = false; }
    } }],
  });
}
