/* The Year: where the money went.
 *
 * Financial years run April to March (Q1 = Apr–Jun). Every sum is the
 * server's, in paise (/api/reports); this only lays it out. Expense heads keep
 * one colour for the whole year — taken from the year's ranking, not the
 * period shown — so switching quarter never repaints a head.
 */
'use strict';

const YearPage = { fy: null, quarter: null, tab: 'entities', fyData: null, data: null };

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
    top.category === 'Uncategorised' ? 'Give entities an expense head (edit them on the Desk) and the year splits by head here.' : 'The split by head returns once spending spreads across heads.');
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
  svg.setAttribute('aria-label', `Spending by expense head: ${shown.map(c => `${c.category} ${Math.round(c.share * 100)}%`).join(', ')}`);
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
    h('div.eyebrow', 'By expense head'),
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
    h('div.panel-head', h('span.eyebrow', 'Month by month, grouped by quarter'), h('span.sub', 'hover a month for its heads · ₹ lakh · choose a quarter to report on it')),
    h('div.qbands', bands), tip);
}

/* ------------------------------------------------------------- ledger */
function ledgerPanel(d, colour) {
  const P = YearPage;
  const q = new URLSearchParams({ fy: String(d.fy) });
  if (d.quarter) q.set('quarter', String(d.quarter));
  const csvKind = { entities: 'entities', heads: 'categories', payees: 'payees', largest: 'cheques' }[P.tab];
  const tabs = seg('Ledger', [['entities', 'Entities'], ['heads', 'Heads'], ['payees', 'Printed payee'], ['largest', 'Largest']], P.tab,
    v => { P.tab = v; const panel = $('.ledger'); panel.replaceWith(ledgerPanel(d, colour)); hydrateIcons($('.ledger')); });
  const max = Math.max(1, ...d.entities.map(e => e.paise), ...d.categories.map(c => c.paise), ...d.payees.map(p => p.paise));
  const vs = (now, before) => {
    if (!before) return h('span.sub', now ? 'new' : '');
    const pct = Math.round((now - before) / before * 100);
    return h('span.sub' + (pct > 0 ? '.up' : pct < 0 ? '.down' : ''), `${pct > 0 ? '+' : ''}${pct}% vs ${d.totals.previous.label.split(' (')[0]}`);
  };
  const bar = (p, c) => h('div.lbar', h('i', { style: { width: (p / max * 100) + '%', background: c } }));
  let rows;
  if (P.tab === 'entities') {
    rows = d.entities.map(e => h('div.lrow',
      h('div', h('div.strong', e.entity_name), h('div.sub', e.payee_name !== e.entity_name ? `pays ${e.payee_name}` : '')),
      h('div.sub', e.category), h('div.sub', plural(e.count, 'cheque')), h('div.mono', `${(e.share * 100).toFixed(1)}%`),
      bar(e.paise, colour(e.category)), h('div.mono.num', rupees(e.paise, { whole: true })), vs(e.paise, e.previous_paise)));
  } else if (P.tab === 'heads') {
    rows = d.categories.map(c => h('div.lrow',
      h('div.strong', h('i.swatch', { style: { background: colour(c.category) } }), c.category),
      h('div.sub', plural(c.entity_count, 'entity', 'entities')), h('div.sub', plural(c.count, 'cheque')), h('div.mono', `${(c.share * 100).toFixed(1)}%`),
      bar(c.paise, colour(c.category)), h('div.mono.num', rupees(c.paise, { whole: true })), vs(c.paise, c.previous_paise)));
  } else if (P.tab === 'payees') {
    rows = d.payees.map(p => h('div.lrow',
      h('div.strong', p.payee), h('div.sub', p.entities.join(', ')), h('div.sub', plural(p.count, 'cheque')), h('div.mono', `${(p.share * 100).toFixed(1)}%`),
      bar(p.paise, 'var(--sel)'), h('div.mono.num', rupees(p.paise, { whole: true })), h('span')));
  } else {
    rows = d.largest.map(c => h('a.lrow', { href: `#/register?leaf=${encodeURIComponent(c.cheque_no)}` },
      h('div', h('div.strong', c.entity_name), h('div.sub', c.payee !== c.entity_name ? c.payee : '')),
      h('div.sub', c.category), h('div.mono', c.cheque_no || '—'), h('div.sub', fmtDay(c.cheque_date)),
      bar(c.amount_paise, colour(c.category)), h('div.mono.num', rupees(c.amount_paise, { always: true })), h('div.sub', c.memo)));
  }
  return h('section.panel.ledger',
    h('div.panel-head', h('span.eyebrow', P.tab === 'payees' ? 'What each bank account received' : 'Who was paid'), tabs,
      h('a.btn.btn-quiet.btn-xs', { href: `/api/reports/${csvKind}.csv?${q}`, download: '' }, 'CSV')),
    P.tab === 'heads' ? headsTable(d) : null,
    h('div.lrows', rows.length ? rows : h('p.empty', 'Nothing in this period.')),
    h('p.foot', 'Counts every cheque written except void and misprinted ones, by cheque date. One-off entities count under Misc unless another head is chosen.'));
}

/* Each head across the quarters (or a quarter's months), with both totals. */
function headsTable(d) {
  const money = p => p ? rupees(p, { whole: true }) : '—';
  return h('table.head-table',
    h('thead', h('tr', h('th', 'Head'), d.columns.map(c => h('th.num', c.label)), h('th.num', 'Total'))),
    h('tbody', d.categories.map(c => h('tr', h('td', c.category),
      c.columns.map(p => h('td.mono.num', money(p))), h('td.mono.num.strong', money(c.paise))))),
    h('tfoot', h('tr', h('th', 'Total'), d.column_totals.map(p => h('th.mono.num', money(p))),
      h('th.mono.num', money(d.totals.paise)))));
}
