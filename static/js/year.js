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

/* What is in is solid; the rest of a quarter is projected on top of it.
   Income is projected up to the usual quarter's (maintenance does not
   change): the last finished quarter that was entered. Expense for the
   quarter in hand and those to come, up to the average of the finished
   quarters. A quarter already past its projection shows what is in. */
function projectQuarters(Q) {
  const qs = Q.quarters;
  const done = qs.filter(q => !q.current && !q.future);
  const doneIn = done.filter(q => q.entered && q.income_paise);
  const anyIn = qs.filter(q => q.entered && q.income_paise);
  const usual = (doneIn.length ? doneIn : anyIn).slice(-1).map(q => q.income_paise)[0];
  const avgExpense = done.length ? Math.round(done.reduce((s, q) => s + q.spent_paise, 0) / done.length) : null;
  let balance = Q.start.closing_paise;
  return qs.map(q => {
    const open = q.current || q.future;
    const incomeIn = q.entered ? q.income_paise : 0;
    const income = usual == null ? incomeIn : open || !q.entered ? Math.max(incomeIn, usual) : incomeIn;
    const expenseIn = q.spent_paise;
    const expense = open && avgExpense != null ? Math.max(expenseIn, avgExpense) : expenseIn;
    const incomeProjected = income > incomeIn, expenseProjected = expense > expenseIn;
    balance += income - expense;
    return Object.assign({}, q, { income, incomeIn, incomeProjected, expense, expenseIn, expenseProjected,
      projected: incomeProjected || expenseProjected, net: income - expense, balance, sinceStart: balance - Q.start.closing_paise });
  });
}

/* The year in view against the accumulated fund it started with (the
   audited fund at 31 March of the year before): what it added, or what it
   took from the fund. Null without that fund or that year's quarters. */
function yearAgainstFund(Q, rows, fy) {
  const fund = (Q.funds || []).find(f => f.fy === fy - 1);
  const mine = rows.filter(r => r.fy === fy);
  if (!fund || !mine.length) return null;
  const income = mine.reduce((s, r) => s + r.income, 0), expense = mine.reduce((s, r) => s + r.expense, 0);
  return { fy, label: `FY ${fy}–${String(fy + 1).slice(2)}`, opening: fund.fund_paise, income, expense, net: income - expense,
    closing: fund.fund_paise + income - expense, projected: mine.some(r => r.projected) };
}

function fundHeadline(y) {
  // Always in lakh, so one sentence never mixes crore and lakh.
  const L = p => `₹${(Math.abs(p) / 1e7).toFixed(1)} lakh`, proj = y.projected ? ' (projected)' : '';
  if (y.net >= 0) return h('p.income-headline',
    h('span.strong', `${y.label}: income covers expense.`),
    ` Income ${L(y.income)}, expense ${L(y.expense)}${proj}: ${L(y.net)} added to the accumulated fund, ${L(y.opening)} → ${L(y.closing)}.`);
  const share = y.opening > 0 ? Math.round(-y.net / y.opening * 100) : null;
  return h('p.income-headline.is-over',
    h('span.strong', `${y.label}: expense runs ${L(y.net)} over income${proj}.`),
    ` ${share != null ? `${share}% of the ${L(y.opening)} accumulated fund used` : 'Taken from the accumulated fund'}, leaving ${y.closing < 0 ? '−' : ''}${L(y.closing)}.`);
}

/* The fund at 1 April, then at 31 March: grown by the year's surplus, or cut
   back by its deficit. One scale for both. */
function fundBar(y) {
  const max = Math.max(1, y.opening, y.closing);
  const pct = p => (Math.max(0, p) / max * 100) + '%';
  const L = p => `${p < 0 ? '−' : ''}₹${(Math.abs(p) / 1e7).toFixed(1)} lakh`;
  const base = Math.min(y.opening, y.closing);
  return h('div.fund-bar',
    h('div.fund-bar-head', h('span', 'Accumulated fund'),
      h('span.mono', `1 April ${y.fy}: ${L(y.opening)} → 31 March ${y.fy + 1}: ${L(y.closing)}${y.projected ? ' projected' : ''}`)),
    h('div.fund-track', { role: 'img', 'aria-label': `Accumulated fund ${L(y.opening)} at the start of ${y.label}, ${L(y.closing)} at its end` },
      h('i.fund-kept', { style: { width: pct(base) } }),
      y.net >= 0
        ? h('i.fund-added' + (y.projected ? '.is-projected' : ''), { style: { width: pct(y.net) } })
        : h('i.fund-used' + (y.projected ? '.is-projected' : ''), { style: { width: pct(y.opening - Math.max(0, y.closing)) } })),
    h('div.fund-bar-foot', h('span.sub', y.net >= 0 ? `+${L(y.net)} this year` : `${L(y.net)} this year`)));
}

function incomePanel(Q) {
  if (!Q) return null;
  const edit = deskCanEnter();
  const tools = edit ? h('div.btn-row',
    Q.start ? h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => enterQuarter(Q) }, 'Enter a quarter…') : null,
    h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => enterFund(Q) }, 'Accumulated fund…'),
    h('button.btn.btn-quiet.btn-xs', { type: 'button', onclick: () => enterStart(Q) }, Q.start ? 'Starting balance…' : 'Set a starting balance…')) : null;
  const head = h('div.panel-head', h('span.eyebrow', 'Income and expense'), tools);
  if (!Q.start) {
    if (!edit) return null;
    return h('section.panel.income-panel', head, h('p.sub', 'Set a starting balance, then enter each quarter’s income.'));
  }
  const rows = projectQuarters(Q);
  const money = p => rupees(p, { whole: true });
  const signed = p => (p < 0 ? '−' : '+') + money(Math.abs(p));
  const end = rows[rows.length - 1];
  // The year in the FY picker, against the fund it started with.
  const fy = YearPage.fy;
  const year = yearAgainstFund(Q, rows, fy);
  const headline = year ? fundHeadline(year) : end ? h('p.income-headline',
    h('span.strong', `${end.sinceStart < 0 ? 'Deficit' : 'Surplus'} of ${lakhs(Math.abs(end.sinceStart), true)}`),
    ` by the end of ${end.label}${rows.some(r => r.projected) ? ', projected' : ''}: balance ${lakhs(end.balance, true)}, from ${lakhs(Q.start.closing_paise, true)} at the start.`) : null;
  const tip = h('div.viz-tip', { hidden: true, role: 'tooltip' });
  const charts = rows.length ? incomeCharts(rows, tip, year) : null;
  const legend = h('ul.ring-legend.income-legend',
    h('li', h('i.is-line', { style: { background: INCOME_COLOUR } }), h('span', 'Income, running total')),
    h('li', h('i.is-line', { style: { background: SPENT_COLOUR } }), h('span', 'Expense, running total')),
    h('li', h('i.is-dash'), h('span', 'Projected')),
    h('li', h('i.is-surplus'), h('span', 'Surplus')),
    h('li', h('i.is-deficit'), h('span', 'Deficit')),
    year ? h('li', h('i.is-fund'), h('span', `Accumulated fund at the start of ${year.label}`)) : null);
  // A projected figure says how much of it is in.
  const cell = (p, inSoFar, projected) => h('td.mono.num' + (projected ? '.is-projected' : ''), money(p),
    projected ? h('span.sub', inSoFar ? ` projected · ${money(inSoFar)} in` : ' projected') : null);
  const table = h('table.head-table.income-table',
    h('thead', h('tr', h('th', 'Quarter'), h('th.num', 'Income'), h('th.num', 'Expense'), h('th.num', 'Net'), h('th.num', 'Balance'))),
    h('tbody',
      h('tr.is-start', h('td', `${Q.start.label} · start`), h('td'), h('td'), h('td'), h('td.mono.num', money(Q.start.closing_paise))),
      rows.map(r => h('tr' + (r.current ? '.is-current' : '') + (r.future ? '.is-future' : ''),
        h('td', r.label, r.current ? h('span.sub', ' · now') : null),
        cell(r.income, r.incomeIn, r.incomeProjected), cell(r.expense, r.expenseIn, r.expenseProjected),
        h('td.mono.num' + (r.net < 0 ? '.is-over' : ''), signed(r.net)),
        h('td.mono.num' + (r.projected ? '.is-projected' : ''), money(r.balance))))));
  return h('section.panel.income-panel', head, headline, charts, rows.length ? legend : null, tip, year ? fundBar(year) : null, table,
    h('p.foot', 'Expense is the cheques written, plus any other expense. Solid is in; dashed is projected. The accumulated fund is from the audited balance sheet. ₹ lakh.'));
}

/* Running totals since the start: income and expense, the gap between them
   shaded as surplus or deficit. Solid is what is in, up to today; dashed is
   projected to the end of the year. Where expense crosses above income the
   shading turns to deficit. */
function incomeCharts(rows, tip, year) {
  const ns = 'http://www.w3.org/2000/svg';
  const el = (name, attrs, text) => {
    const n = document.createElementNS(ns, name);
    Object.entries(attrs || {}).forEach(([k, v]) => n.setAttribute(k, String(v)));
    if (text != null) n.textContent = text;
    return n;
  };
  const W = 1000, H = 300, padL = 48, padR = 150, top = 18, base = H - 30;
  const n = rows.length, slot = (W - padL - padR) / n;
  const xAt = k => padL + slot * k;                       // k = 0 is the start, k = n the end of the last quarter
  const lakh = p => (p / 1e7).toFixed(1);
  const nice = m => { const unit = Math.pow(10, Math.floor(Math.log10(m || 1))) / 5; return Math.ceil((m || 1) / unit) * unit; };

  // One set of points for both lines and the gap: the running totals of what
  // is in, quarter by quarter up to today, then the projection to the end.
  // Today sits inside the current quarter, as far along as the calendar is.
  const today = todayISO();
  const elapsed = r => {
    const q = r.quarter, startMonth = [4, 7, 10, 1][q - 1], year = q === 4 ? r.fy + 1 : r.fy;
    const from = new Date(year, startMonth - 1, 1), to = new Date(year, startMonth + 2, 1), now = new Date(today + 'T12:00:00');
    return Math.min(1, Math.max(0, (now - from) / (to - from)));
  };
  const pts = [{ k: 0, x: xAt(0), inc: 0, exp: 0 }];
  let inc = 0, exp = 0, split = null;
  rows.forEach((r, i) => {
    const open = r.current || r.future || r.incomeProjected || r.expenseProjected;
    if (open && split === null) {
      split = pts.length - 1;
      if (r.current) {
        pts.push({ x: xAt(i) + slot * elapsed(r), inc: inc + r.incomeIn, exp: exp + r.expenseIn, now: true });
        split = pts.length - 1;
      }
    }
    inc += r.income; exp += r.expense;
    pts.push({ k: i + 1, x: xAt(i + 1), inc, exp });
  });
  if (split === null) split = pts.length - 1;

  // The year in view, and the cushion above its income line: how far expense
  // could run over income before the fund it started with is used up.
  let band = null;
  if (year) {
    const i0 = rows.findIndex(r => r.fy === year.fy), i1 = rows.map(r => r.fy).lastIndexOf(year.fy);
    const from = pts.findIndex(p => p.k === i0), to = pts.findIndex(p => p.k === i1 + 1);
    if (from >= 0 && to > from && year.opening > 0) {
      const carried = pts[from].inc - pts[from].exp;
      band = pts.slice(from, to + 1).map(p => ({ x: p.x, inc: p.inc, top: p.inc - carried + year.opening }));
    }
  }
  const max = nice(Math.max(1, ...pts.map(p => Math.max(p.inc, p.exp)), ...(band || []).map(b => b.top)));
  const y = p => base - (p / max) * (base - top);
  const svg = el('svg', { viewBox: `0 0 ${W} ${H}`, class: 'ic-chart', role: 'img',
    'aria-label': 'Running totals of income and expense since the start, with the surplus or deficit between them; the table below has the figures' });

  [0, max / 2, max].forEach(t => {
    svg.appendChild(el('line', { x1: padL, x2: W - padR, y1: y(t), y2: y(t), class: t === 0 ? 'ic-zero' : 'ic-grid' }));
    svg.appendChild(el('text', { x: padL - 6, y: y(t) + 4, 'text-anchor': 'end', class: 'ic-tick' }, lakh(t)));
  });
  rows.forEach((r, i) => {
    svg.appendChild(el('line', { x1: xAt(i + 1), x2: xAt(i + 1), y1: base, y2: base + 4, class: 'ic-zero' }));
    svg.appendChild(el('text', { x: xAt(i) + slot / 2, y: H - 10, 'text-anchor': 'middle', class: 'ic-label' + (r.current ? ' is-now' : '') },
      `Q${r.quarter} ${String(r.fy).slice(2)}–${String(r.fy + 1).slice(2)}${r.current ? ' · now' : ''}`));
  });

  if (band) {
    const defs = el('defs');
    const pat = el('pattern', { id: 'fundHatch', patternUnits: 'userSpaceOnUse', width: 7, height: 7, patternTransform: 'rotate(45)' });
    pat.appendChild(el('line', { x1: 0, y1: 0, x2: 0, y2: 7, class: 'ic-hatch' }));
    defs.appendChild(pat);
    svg.appendChild(defs);
    const first = band[0], last = band[band.length - 1];
    svg.appendChild(el('rect', { x: first.x, y: top, width: last.x - first.x, height: base - top, class: 'ic-year' }));
    svg.appendChild(el('polygon', { class: 'ic-fund', fill: 'url(#fundHatch)',
      points: band.map(b => [b.x, y(b.inc)].join(',')).concat(band.slice().reverse().map(b => [b.x, y(b.top)].join(','))).join(' ') }));
    svg.appendChild(el('polyline', { class: 'ic-fund-edge', fill: 'none', points: band.map(b => [b.x, y(b.top)].join(',')).join(' ') }));
    const mid = band[Math.floor(band.length / 2)];
    svg.appendChild(el('text', { x: mid.x, y: (y(mid.top) + y(mid.inc)) / 2 + 4, 'text-anchor': 'middle', class: 'ic-fund-label' },
      `Accumulated fund ${(year.opening / 1e7).toFixed(1)}`));
  }

  // The gap between the lines, split where they cross.
  const shade = (a, b, projected) => {
    const surplus = (a.inc - a.exp) + (b.inc - b.exp) >= 0;
    svg.appendChild(el('polygon', { points: [[a.x, y(a.inc)], [b.x, y(b.inc)], [b.x, y(b.exp)], [a.x, y(a.exp)]].map(p => p.join(',')).join(' '),
      class: 'ic-gap ' + (surplus ? 'is-surplus' : 'is-deficit') + (projected ? ' is-projected' : '') }));
  };
  for (let j = 1; j < pts.length; j++) {
    const a = pts[j - 1], b = pts[j], d1 = a.inc - a.exp, d2 = b.inc - b.exp, projected = j > split;
    if (d1 * d2 < 0) {
      const t = d1 / (d1 - d2), c = { x: a.x + (b.x - a.x) * t, inc: a.inc + (b.inc - a.inc) * t };
      c.exp = c.inc;
      shade(a, c, projected); shade(c, b, projected);
    } else shade(a, b, projected);
  }

  const series = (key, colour) => {
    const line = (list, extra) => { if (list.length > 1) svg.appendChild(el('polyline', { points: list.map(p => [p.x, y(p[key])].join(',')).join(' '), fill: 'none', stroke: colour, class: 'ic-run' + extra })); };
    line(pts.slice(0, split + 1), '');
    line(pts.slice(split), ' is-projected');
    pts.forEach((p, j) => {
      if (!j) return;
      if (p.now) svg.appendChild(el('circle', { cx: p.x, cy: y(p[key]), r: 4.5, fill: colour, class: 'ic-now-dot' }));
      else svg.appendChild(el('circle', { cx: p.x, cy: y(p[key]), r: 3, stroke: colour, class: 'ic-q-dot' + (j > split ? ' is-projected' : '') }));
    });
    return y(pts[pts.length - 1][key]);
  };
  const yi = series('inc', INCOME_COLOUR), ye = series('exp', SPENT_COLOUR);
  const endInc = pts[pts.length - 1].inc, endExp = pts[pts.length - 1].exp;
  const nowPt = pts.find(p => p.now);
  if (nowPt) svg.appendChild(el('text', { x: nowPt.x, y: top - 4, 'text-anchor': 'middle', class: 'ic-tick' }, 'today'));
  if (nowPt) svg.appendChild(el('line', { x1: nowPt.x, x2: nowPt.x, y1: top, y2: base, class: 'ic-today' }));

  // Direct labels at the end, kept apart if the lines finish close together.
  const projectedEnd = rows.some(r => r.projected);
  let li = yi, le = ye;
  if (Math.abs(li - le) < 30) { const mid = (li + le) / 2, up = li < le ? -1 : 1; li = mid + up * 15; le = mid - up * 15; }
  const endX = xAt(n) + 8;
  svg.appendChild(el('text', { x: endX, y: li + 4, class: 'ic-end' }, `Income ${lakh(endInc)}`));
  svg.appendChild(el('text', { x: endX, y: le + 4, class: 'ic-end' }, `Expense ${lakh(endExp)}`));
  const gap = endInc - endExp;
  svg.appendChild(el('text', { x: endX, y: Math.max(li, le) + 24, class: 'ic-end ' + (gap < 0 ? 'is-over' : 'is-good') },
    `${gap < 0 ? 'Deficit' : 'Surplus'} ${lakh(Math.abs(gap))}${projectedEnd ? ' projected' : ''}`));

  // One hover target per quarter.
  const hits = h('div.ic-hits');
  rows.forEach((r, i) => {
    const hit = h('div.ic-hit', { style: { left: (xAt(i) / W * 100) + '%', width: (slot / W * 100) + '%' } });
    hit.addEventListener('mouseenter', () => {
      const line = (label, colour, total, sofar, projected) => h('div.tip-row', h('i', { style: { background: colour } }),
        h('span', label + (projected ? (sofar ? `, ${rupees(sofar, { whole: true })} in` : ', projected') : '')), h('span.mono', rupees(total, { whole: true })));
      const end = pts.find(p => p.k === i + 1), since = end.inc - end.exp;
      clear(tip, h('div.strong', r.label + (r.current ? ' · now' : '')),
        line('Income', INCOME_COLOUR, r.income, r.incomeIn, r.incomeProjected),
        line('Expense', SPENT_COLOUR, r.expense, r.expenseIn, r.expenseProjected),
        h('div.tip-row', h('span'), h('span', since < 0 ? 'Deficit since the start' : 'Surplus since the start'), h('span.mono', rupees(Math.abs(since), { whole: true }))),
        h('div.tip-row', h('span'), h('span', 'Balance'), h('span.mono', rupees(r.balance, { whole: true }))));
      tip.hidden = false;
      const b = hit.getBoundingClientRect(), pr = hit.closest('.income-panel').getBoundingClientRect();
      Object.assign(tip.style, { left: (b.left - pr.left + b.width / 2) + 'px', top: (b.top - pr.top + 90) + 'px' });
    });
    hit.addEventListener('mouseleave', () => { tip.hidden = true; });
    hits.appendChild(hit);
  });
  return h('div.ic-wrap', svg, h('div.ic-hits-wrap', hits));
}

/* The quarters that can be entered: after the starting point, up to now. */
async function enterQuarter(Q) {
  const pick = h('select', { 'aria-label': 'Quarter' }, Q.quarters.filter(q => !q.future).reverse().map(q =>
    h('option', { value: `${q.fy}-${q.quarter}` }, q.label)));
  const income = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off' });
  const outside = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off' });
  const note = h('p.field-note');
  const fill = () => {
    const q = Q.quarters.find(x => `${x.fy}-${x.quarter}` === pick.value);
    income.value = q && q.entered ? plainAmount(q.income_paise) : '';
    outside.value = q && q.outside_paise ? plainAmount(q.outside_paise) : '';
    note.textContent = q ? `Cheques: ${rupees(q.register_paise, { whole: true })}` : '';
  };
  pick.addEventListener('change', fill);
  fill();
  const err = h('p.form-error', { hidden: true });
  await modal({
    eyebrow: 'Income and expense', title: 'Enter a quarter',
    body: h('div', field('Quarter', pick, note),
      field('Income', income),
      field('Other expense', outside, 'Anything not paid by a cheque in Banyan.'),
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

/* The accumulated fund at 31 March of a year that has ended, from its audited balance sheet. */
async function enterFund(Q) {
  const cur = Q.current.fy;
  const years = [];
  for (let fy = cur - 1; fy > cur - 8; fy--) years.push(fy);
  const label = fy => `31 March ${fy + 1} (end of FY ${fy}–${String(fy + 1).slice(2)})`;
  const pick = h('select', { 'aria-label': 'Year' }, years.map(fy => h('option', { value: String(fy) }, label(fy))));
  const amount = h('input.mono', { type: 'text', inputmode: 'decimal', autocomplete: 'off' });
  const fill = () => {
    const f = (Q.funds || []).find(x => x.fy === Number(pick.value));
    amount.value = f ? (f.fund_paise < 0 ? '-' : '') + plainAmount(Math.abs(f.fund_paise)) : '';
  };
  pick.addEventListener('change', fill);
  fill();
  const err = h('p.form-error', { hidden: true });
  await modal({
    eyebrow: 'Income and expense', title: 'Accumulated fund',
    body: h('div', field('As at', pick), field('Accumulated fund', amount, 'From the audited balance sheet.'), err),
    onOpen: () => amount.focus(),
    actions: [{ label: 'Cancel', value: 'cancel' }, { label: 'Save', primary: true, value: 'save', onClick: async (ev, dlg) => {
      ev.preventDefault();
      try {
        await post('/api/quarters/fund', { fy: Number(pick.value), fund: amount.value.trim() });
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
    eyebrow: 'Income and expense', title: 'Starting balance',
    body: h('div', field('Quarter', pick),
      field('Balance at the end of it', closing), err),
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
