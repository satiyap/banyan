/* The leaf on screen.
 *
 * Every leaf this page shows is drawn by ChequeLayout.render — the same call,
 * on the same calibration, that the print renderer makes — over the flattened
 * photograph of a real leaf, or a drawn guide when there is none. Nothing here
 * places a printed field itself. The only geometry this file reads is the
 * calibration's own field boxes, to lay transparent inputs over them; so a
 * calibration edit moves the inputs and the ink together.
 */
'use strict';

const MMPX = 96 / 25.4;
const liveLeaves = new Set();

/* What is already on the leaf, under the fields this app prints. With a
   photograph only the printable limit and, when something comes close, the
   MICR band are drawn; without one, the leaf's rules are drawn from the
   calibration. No account, cheque or MICR number is ever drawn. */
function drawGuide(g, cal, photo) {
  const F = cal.fields;
  const mm = v => `${v}mm`;
  g.replaceChildren();
  const put = (cls, styles, text) => {
    const n = h('div.g' + (cls ? '.' + cls : ''), text == null ? null : String(text));
    Object.assign(n.style, styles);
    g.appendChild(n);
  };
  if (!photo) {
    put('g-strong', { left: mm(9), top: mm(3), fontSize: mm(4.1) }, 'STATE BANK OF INDIA');
    put('', { left: mm(9), top: mm(8.4), fontSize: mm(2.1) }, 'Branch name and address');
    for (let i = 0; i < 8; i++) {
      const cx = F.date.x + i * F.date.pitch;
      put('g-box', { left: mm(cx - F.date.pitch / 2 + 0.35), top: mm(F.date.y - 1.4),
                     width: mm(F.date.pitch - 0.7), height: mm(F.date.size + 2.6) });
    }
    put('', { left: mm(F.date.x - F.date.pitch / 2), top: mm(F.date.y - 4.4), fontSize: mm(1.9) },
        'D D  M M  Y Y Y Y');
    put('g-strong', { left: mm(8), top: mm(19.5), fontSize: mm(3) }, 'PAY');
    put('g-rule', { left: mm(17), top: mm(23.6), width: mm(164) });
    put('g-strong', { left: mm(173), top: mm(19.8), fontSize: mm(2.8) }, 'OR ORDER');
    put('g-strong', { left: mm(8), top: mm(27.4), fontSize: mm(3) }, 'RUPEES');
    put('g-rule', { left: mm(24), top: mm(31.5), width: mm(174) });
    put('g-rule', { left: mm(8), top: mm(39.2), width: mm(145) });
    put('g-box', { left: mm(153), top: mm(31.8), width: mm(47), height: mm(8.2) });
    put('', { left: mm(155), top: mm(33.4), fontSize: mm(4) }, '₹');
    put('g-sign', { left: mm(158), top: mm(66), fontSize: mm(2.4), width: mm(40) }, 'Authorised signatory');
  }
  const inset = cal.hardware_inset_mm;
  put('g-limit', { left: mm(inset), top: mm(inset), width: mm(204 - inset * 2), height: mm(92 - inset * 2) });
  const micrTop = cal.micr_top_mm;
  const lowest = Math.max(...Object.values(F).map(f => (f.y || 0) + (f.size || f.h || 0) + 2)) + (cal.dy || 0);
  if (lowest > micrTop - 12) {
    put('g-micr', { left: 0, top: mm(micrTop), width: mm(204), height: mm(92 - micrTop) });
    put('g-micr-label', { left: mm(3), top: mm(micrTop + 1.2), fontSize: mm(2.6) },
        `MICR band — keep clear (a field reaches ${lowest.toFixed(1)}mm)`);
  }
}

/* The calibrated box of each field the treasurer types into, in leaf mm,
   with the global nudge applied as print applies it. */
function fieldBoxes(cal) {
  const F = cal.fields, dx = cal.dx || 0, dy = cal.dy || 0;
  return {
    date: { x: F.date.x - F.date.pitch / 2 + dx, y: F.date.y - 1.2 + dy,
            w: F.date.pitch * 8, h: F.date.size + 2.4 },
    payee: { x: F.payee.x + dx, y: F.payee.y - 0.8 + dy, w: F.payee.w, h: F.payee.size + 2.2 },
    words1: { x: F.words1.x + dx, y: F.words1.y - 0.8 + dy, w: F.words1.w, h: F.words1.size + 2.2 },
    words2: { x: F.words2.x + dx, y: F.words2.y - 0.8 + dy, w: F.words2.w, h: F.words2.size + 2.2 },
    amount: { x: F.amount.x + dx, y: F.amount.y - 1 + dy, w: 44, h: F.amount.size + 2.4 },
  };
}

/* The construction view over a leaf, `s` screen pixels per leaf millimetre.
   Every number comes from the calibration the server serves: the boxes are
   fieldBoxes() (the global nudge applied, as print applies it), the margin is
   the printer's declared inset and the band is the MICR top. */
const CX_NAMES = { date: 'Date', payee: 'Payee', words1: 'Words, line 1', words2: 'Words, line 2', amount: 'Amount', cross: 'A/C payee crossing' };
function paintConstruct(layer, legend, cal, s, view) {
  const W = ChequeLayout.CHQ_W, H = ChequeLayout.CHQ_H;
  const px = v => `${(v * s).toFixed(2)}px`;
  const f1 = v => (Math.round(v * 10) / 10).toFixed(1);
  const first = !layer.dataset.drawn;
  const nodes = [];
  const put = (cls, style, ...kids) => {
    const n = h('div.' + cls, ...kids);
    for (const [k, v] of Object.entries(style)) { if (k.startsWith('--')) n.style.setProperty(k, v); else n.style[k] = v; }
    nodes.push(n); return n;
  };

  // Rulers: a tick every 5 mm, longer every 10, a figure every 20 across and every 10 down.
  const top = put('cx-ruler.cx-top', {});
  for (let x = 0; x <= W; x += 5) {
    const t = h('i' + (x % 10 ? '' : '.major'), { style: { left: px(x) } });
    top.appendChild(t);
    if (x % 20 === 0) top.appendChild(h('span', { style: { left: px(x) } }, String(x)));
  }
  top.appendChild(h('i.major', { style: { left: px(W) } }));
  const left = put('cx-ruler.cx-left', {});
  for (let y = 0; y <= H; y += 5) {
    left.appendChild(h('i' + (y % 10 ? '' : '.major'), { style: { top: px(y) } }));
    if (y % 10 === 0) left.appendChild(h('span', { style: { top: px(y) } }, String(y)));
  }

  // The printer's unprintable margin, hatched on all four edges.
  const m = cal.hardware_inset_mm;
  put('cx-margin', { left: 0, top: 0, width: px(W), height: px(m) });
  put('cx-margin', { left: 0, top: px(H - m), width: px(W), height: px(m) });
  put('cx-margin', { left: 0, top: px(m), width: px(m), height: px(H - 2 * m) });
  put('cx-margin', { left: px(W - m), top: px(m), width: px(m), height: px(H - 2 * m) });

  // The MICR band: nothing dynamic may enter it.
  const micr = cal.micr_top_mm;
  put('cx-micr', { left: 0, top: px(micr), width: px(W), height: px(H - micr) },
    h('span', `MICR band from ${f1(micr)} mm, kept clear`));

  // Each calibrated box, numbered, with leaders back to both rulers.
  const boxes = Object.entries(fieldBoxes(cal));
  const cr = cal.fields.cross;
  const rows = [];
  boxes.forEach(([k, b], i) => {
    const n = i + 1;
    const box = put('cx-box', { left: px(b.x), top: px(b.y), width: px(b.w), height: px(b.h), '--i': i });
    box.dataset.k = k;
    if (k === 'date') {
      const p = cal.fields.date.pitch;
      for (let c = 1; c < 8; c++) box.appendChild(h('i.cx-cell', { style: { left: px(c * p) } }));
    }
    box.appendChild(h('b.cx-badge', String(n)));
    const lx = put('cx-lead.cx-lead-x', { left: px(b.x), top: '0px', height: px(b.y) }, h('span', f1(b.x)));
    const ly = put('cx-lead.cx-lead-y', { left: '0px', top: px(b.y), width: px(b.x) }, h('span', f1(b.y)));
    lx.dataset.k = k; ly.dataset.k = k;
    rows.push([k, n, b.x, b.y, b.w, b.h]);
  });
  if (cr) {
    const n = boxes.length + 1;
    const x = cr.x + (cal.dx || 0), y = cr.y + (cal.dy || 0);
    const box = put('cx-box.cx-cross', { left: px(x), top: px(y), width: px(cr.w), height: px(cr.h), '--i': boxes.length,
      transform: `rotate(${cr.angle}deg)`, transformOrigin: cr.origin || 'top left' });
    box.dataset.k = 'cross';
    box.appendChild(h('b.cx-badge', String(n)));
    rows.push(['cross', n, x, y, cr.w, cr.h, cr.angle]);
  }
  layer.replaceChildren(...nodes);
  layer.querySelectorAll('.cx-box').forEach(b => {
    b.addEventListener('mouseenter', () => view.hot(b.dataset.k));
    b.addEventListener('mouseleave', () => view.hot(null));
  });
  if (first) { layer.classList.add('is-drawing'); layer.dataset.drawn = '1'; setTimeout(() => layer.classList.remove('is-drawing'), 1400); }

  // The measurements, as a table the boxes answer to.
  const cell = v => h('span.num', f1(v));
  const table = h('div.cx-table', { role: 'table', 'aria-label': 'Calibrated field positions in millimetres' },
    h('div.cx-row.cx-head', { role: 'row' }, ...['', 'Field', 'x', 'y', 'w', 'h'].map(t => h('span', { role: 'columnheader' }, t))),
    ...rows.map(([k, n, x, y, w, hh, a]) => {
      const r = h('div.cx-row', { role: 'row', tabIndex: 0, 'data-k': k },
        h('b.cx-badge', String(n)), h('span', CX_NAMES[k] + (a != null ? `, ${a}°` : '')), cell(x), cell(y), cell(w), cell(hh));
      r.addEventListener('mouseenter', () => view.hot(k)); r.addEventListener('mouseleave', () => view.hot(null));
      r.addEventListener('focus', () => view.hot(k)); r.addEventListener('blur', () => view.hot(null));
      return r;
    }));
  const facts = h('p.cx-facts',
    `Leaf ${W} × ${H} mm, printed rotated onto a ${ChequeLayout.PAGE_W} × ${ChequeLayout.PAGE_H} mm page. `,
    `Printer margin ${m} mm on every edge (hatched). MICR band from ${f1(micr)} mm. `,
    `Nudge dx ${f1(cal.dx || 0)}, dy ${f1(cal.dy || 0)} mm. Calibration v${cal.version}.`);
  legend.replaceChildren(table, facts);
}

/* A leaf that fits its container's width. `stamp` puts a register stamp
   (VOID, PRINTED…) across it; `test` draws it as a plain-paper test sheet. */
function makeLeaf(opts = {}) {
  const wrap = h('div.leaf-wrap' + (opts.small ? '.leaf-small' : ''));
  const scaler = h('div.leaf-scaler');
  const leaf = h('div.leaf' + (opts.test ? '.is-test' : ''));
  const guide = h('div.leaf-guide', { 'aria-hidden': 'true' });
  const mount = h('div.leaf-mount');
  const boxes = h('div.leaf-boxes', { 'aria-hidden': 'true' });
  const overlay = h('div.leaf-overlay');
  const stamp = h('div.leaf-stamp', { 'aria-hidden': 'true', hidden: true });
  leaf.append(guide, mount, boxes, overlay, stamp);
  scaler.appendChild(leaf);
  wrap.appendChild(scaler);
  // Proof-sheet dimension guides beside the full-size preview. Preview-only:
  // they sit outside .leaf, so nothing here can reach the printed page.
  if (!opts.small) {
    wrap.append(
      h('span.leaf-dim.dim-w', { 'aria-hidden': 'true' }, h('b', `${ChequeLayout.CHQ_W} mm`)),
      h('span.leaf-dim.dim-h', { 'aria-hidden': 'true' }, h('b', `${ChequeLayout.CHQ_H} mm`)));
  }
  // The construction view: rulers, the printer's margin, the MICR band and
  // every calibrated box, drawn in screen pixels over the scaled leaf so the
  // readouts stay legible. Preview-only, outside .leaf, read from the live
  // calibration and never a second copy of it.
  const construct = opts.construct ? h('div.leaf-construct', { 'aria-hidden': 'true', hidden: true }) : null;
  if (construct) wrap.appendChild(construct);

  const view = {
    el: wrap, leaf, overlay, mount, payload: null, composed: null, scale: 1,
    onLayout: null,
    draw(payload) {
      view.payload = payload;
      if (!State.cal) return null;
      view.composed = ChequeLayout.render(mount, State.cal, payload);
      return view.composed;
    },
    setStamp(text, tone) {
      stamp.hidden = !text;
      stamp.textContent = text || '';
      stamp.className = 'leaf-stamp' + (tone ? ' tone-' + tone : '');
    },
    showBoxes(on) {
      boxes.replaceChildren();
      if (!on || !State.cal) return;
      Object.entries(fieldBoxes(State.cal)).forEach(([name, b]) => {
        const n = h('div.fbox', h('span', name));
        Object.assign(n.style, { left: b.x + 'mm', top: b.y + 'mm', width: b.w + 'mm', height: b.h + 'mm' });
        boxes.appendChild(n);
      });
    },
    relayout() {
      if (!State.cal) return;
      leaf.classList.toggle('has-photo', !!State.photo);
      drawGuide(guide, State.cal, State.photo);
      if (view.payload) view.draw(view.payload);
      if (view.boxesOn) view.showBoxes(true);
      view.paintConstruct();
      if (view.onLayout) view.onLayout();
    },
    fit() {
      const w = wrap.clientWidth;
      if (!w) return;
      view.scale = w / (204 * MMPX);
      scaler.style.transform = `scale(${view.scale})`;
      wrap.style.height = `${92 * MMPX * view.scale}px`;
      view.paintConstruct();
    },
    constructOn: false,
    legend: construct ? h('div.cx-legend', { hidden: true }) : null,
    setConstruct(on) {
      if (!construct) return;
      view.constructOn = on;
      construct.hidden = !on;
      view.legend.hidden = !on;
      wrap.classList.toggle('is-construct', on);
      wrap.closest('.mat')?.classList.toggle('is-construct', on);
      view.paintConstruct();
    },
    hot(key) {
      if (!construct) return;
      construct.querySelectorAll('[data-k]').forEach(n => n.classList.toggle('is-hot', n.dataset.k === key));
      view.legend.querySelectorAll('[data-k]').forEach(n => n.classList.toggle('is-hot', n.dataset.k === key));
    },
    paintConstruct() {
      if (!construct || !view.constructOn || !State.cal || !wrap.clientWidth) return;
      paintConstruct(construct, view.legend, State.cal, MMPX * view.scale, view);
    },
  };
  new ResizeObserver(() => view.fit()).observe(wrap);
  liveLeaves.add(view);
  view.relayout();
  return view;
}

on('cal', () => {
  for (const v of liveLeaves) {
    if (!v.el.isConnected) { liveLeaves.delete(v); continue; }
    v.relayout();
  }
});

/* A cheque payload the renderer understands, from what is known so far. */
function leafPayload(d, composed) {
  return {
    payee: d.payee || '', cheque_date: d.cheque_date || '',
    amount_words: composed ? composed.amount_words : '',
    amount_figures: composed ? composed.amount_figures : '',
    crossing: d.crossing !== false, strike_order: !!d.strike_order, star_fill: !!d.star_fill,
  };
}

/* Server-side composition (amount parsing and words), cached by input. Only
   the server turns typed rupees into words; this never guesses. */
const composeCache = new Map();
async function composeFor(d) {
  const body = {
    payee: d.payee || '-', amount: d.amount, cheque_date: d.cheque_date,
    crossing: d.crossing !== false, strike_order: !!d.strike_order, star_fill: !!d.star_fill,
  };
  const key = JSON.stringify(body);
  if (composeCache.has(key)) return composeCache.get(key);
  const p = post('/api/compose', body).catch(err => ({ ok: false, error: err.message }));
  composeCache.set(key, p);
  if (composeCache.size > 300) composeCache.delete(composeCache.keys().next().value);
  return p;
}

/* What the print path would say about a cheque: the server's warnings and
   the layout's own fit check, which is the one print uses. */
function fitReport(d, res) {
  const out = { ok: !!(res && res.ok), issues: [], notes: [], wordsSize: null };
  if (!res) return out;
  if (!res.ok) { out.issues.push(res.error); return out; }
  (res.warnings || []).forEach(w => out.notes.push(w));
  if (State.cal) {
    const c = ChequeLayout.compose(State.cal, leafPayload(d, res));
    c.warnings.forEach(w => out.issues.push(w));
    if (c.wordsSize < State.cal.fields.words1.size) out.wordsSize = c.wordsSize;
    if (c.payeeSize < State.cal.fields.payee.size) out.payeeSize = c.payeeSize;
  }
  return out;
}
