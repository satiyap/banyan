/* Banyan Cheque Desk — shared core.
 *
 * Plain scripts, no build step: each file adds one place to the page and
 * shares this file's helpers. User-supplied text is only ever set with
 * textContent (h() below does that for every string child); a payee name is
 * data, never markup.
 */
'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/* h('div.a.b', {onclick, title, dataset: {}}, child, 'text', [more]) */
function h(sel, props, ...kids) {
  const [tag, ...classes] = sel.split('.');
  const n = document.createElement(tag || 'div');
  if (classes.length) n.className = classes.join(' ');
  if (props && (typeof props !== 'object' || props instanceof Node || Array.isArray(props))) {
    kids.unshift(props);
    props = null;
  }
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') n.addEventListener(k.slice(2), v);
    else if (k === 'dataset') Object.assign(n.dataset, v);
    else if (k === 'style' && typeof v === 'object') Object.assign(n.style, v);
    else if (k === 'text') n.textContent = v;
    else if (k === 'class') n.className += (n.className ? ' ' : '') + v;
    else if (k in n && typeof v !== 'string') n[k] = v;
    else n.setAttribute(k, v === true ? '' : v);
  }
  append(n, kids);
  return n;
}

function append(n, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    n.appendChild(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return n;
}

function clear(n, ...kids) { n.replaceChildren(); return append(n, kids); }

/* ------------------------------------------------------------- API */
/* The version this page was served as. Every call carries it; a server that
   has moved on refuses the call and the page reloads rather than talk to an
   API it was not written for. */
const DESK_VERSION = (document.querySelector('meta[name="desk-version"]') || {}).content || '';

async function api(path, options = {}) {
  const opts = Object.assign({}, options);
  if (opts.body && typeof opts.body !== 'string') opts.body = JSON.stringify(opts.body);
  opts.headers = Object.assign({ 'Content-Type': 'application/json', 'X-Desk-Version': DESK_VERSION },
                               opts.headers || {});
  const res = await fetch(path, opts);
  const body = await res.json().catch(() => ({}));
  if (res.status === 409 && body && body.reload) {
    deskUpdated(body.desk_version);
    throw new Error(body.detail);
  }
  if (!res.ok) {
    const d = body && body.detail;
    const msg = typeof d === 'string' ? d
      : Array.isArray(d) ? d.map(x => x.msg).join('; ') : `request failed (${res.status})`;
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return body;
}
const post = (path, body) => api(path, { method: 'POST', body: body || {} });

/* Reload straight away when nothing would be lost; with something half
   typed, ask first. */
function deskUpdated(version) {
  const target = `${location.pathname}?v=${encodeURIComponent(version || '')}${location.hash}`;
  if (location.search === `?v=${encodeURIComponent(version || '')}`) return;
  const busy = State.dirty || $$('dialog').some(d => d.open);
  if (!busy) { location.replace(target); return; }
  $('#btnReload').onclick = () => location.replace(target);
  $('#updateBar').hidden = false;
}

/* ------------------------------------------------------------- money */
/* Paise are integers everywhere; these only lay them out. */
function groupIndian(n) {
  const s = String(n);
  if (s.length <= 3) return s;
  let head = s.slice(0, -3);
  const parts = [];
  while (head.length > 2) { parts.unshift(head.slice(-2)); head = head.slice(0, -2); }
  if (head) parts.unshift(head);
  return parts.join(',') + ',' + s.slice(-3);
}

function rupees(paise, opts = {}) {
  const sign = paise < 0 ? '-' : '';
  const abs = Math.abs(paise || 0);
  const frac = abs % 100;
  const show = opts.always || (frac && !opts.whole);
  return `${sign}₹${groupIndian(Math.floor(abs / 100))}${show ? '.' + String(frac).padStart(2, '0') : ''}`;
}

/* ₹38.6L / ₹5.9 lakh / ₹1.2Cr — for headlines, never for a cheque. */
function lakhs(paise, long) {
  const r = (paise || 0) / 100;
  if (r >= 1e7) return `₹${(r / 1e7).toFixed(r >= 1e8 ? 0 : 1)}${long ? ' crore' : 'Cr'}`;
  if (r >= 1e5) return `₹${(r / 1e5).toFixed(1)}${long ? ' lakh' : 'L'}`;
  return rupees(paise, { whole: true });
}

/* Rupee text typed by a person -> paise, or null. Only for checks shown on
   screen; the server parses every amount it records. */
function paiseOf(text) {
  const t = String(text || '').replace(/[₹,\s]/g, '').replace(/\/-$/, '');
  const m = /^(\d{1,9})(?:\.(\d{0,2}))?$/.exec(t);
  if (!m) return null;
  return Number(m[1]) * 100 + Number((m[2] || '').padEnd(2, '0') || 0);
}

/* Rupees as they are typed in a row: whole rupees carry no ".00"; paise show
   only when there are some. */
function plainAmount(paise) {
  const frac = paise % 100;
  return `${Math.floor(paise / 100)}${frac ? '.' + String(frac).padStart(2, '0') : ''}`;
}

/* ------------------------------------------------------------- dates */
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const MONTHS_LONG = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August',
                     'September', 'October', 'November', 'December'];
const DAYS_LONG = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

function iso(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
function parseISO(s) { const [y, m, d] = s.split('-').map(Number); return new Date(y, m - 1, d); }
function todayISO() { return (State.desk && State.desk.today) || iso(new Date()); }
function addDays(s, n) { const d = parseISO(s); d.setDate(d.getDate() + n); return iso(d); }
function daysBetween(a, b) { return Math.round((parseISO(b) - parseISO(a)) / 864e5); }
function fmtDay(s) { if (!s) return ''; const d = parseISO(s); return `${String(d.getDate()).padStart(2, '0')} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`; }
function fmtShort(s) { if (!s) return ''; const d = parseISO(s); return `${String(d.getDate()).padStart(2, '0')} ${MONTHS[d.getMonth()]}`; }
function fmtLong(s) { const d = parseISO(s); return `${DAYS_LONG[d.getDay()]} ${d.getDate()} ${MONTHS_LONG[d.getMonth()]} ${d.getFullYear()}`; }
function fmtStamp(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  return `${String(d.getDate()).padStart(2, '0')} ${MONTHS[d.getMonth()]} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}
function fyOf(s) { const d = parseISO(s); return d.getMonth() >= 3 ? d.getFullYear() : d.getFullYear() - 1; }
function fyLabel(fy) { return `FY ${fy}–${String(fy + 1).slice(-2)}`; }
function quarterOf(s) { return Math.floor(((parseISO(s).getMonth() - 3 + 12) % 12) / 3) + 1; }

function relDays(n) {
  if (n === 0) return 'today';
  if (n === 1) return 'tomorrow';
  if (n === -1) return 'yesterday';
  return n > 0 ? `in ${n}d` : `${-n}d late`;
}

const NUMBER_WORDS = ['No', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight',
                      'Nine', 'Ten', 'Eleven', 'Twelve'];
function countWord(n, lower) {
  const w = n < NUMBER_WORDS.length ? NUMBER_WORDS[n] : String(n);
  return lower ? w.toLowerCase() : w;
}
function plural(n, one, many) { return `${n} ${n === 1 ? one : (many || one + 's')}`; }

/* ------------------------------------------------------------- misc */
let toastTimer;
function toast(message, bad) {
  const t = $('#toast');
  t.textContent = message;
  t.classList.toggle('bad', !!bad);
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), bad ? 7000 : 4200);
}

function debounce(fn, ms) {
  let timer;
  return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); };
}

function stored(key, fallback) {
  try { const v = localStorage.getItem(key); return v ? JSON.parse(v) : fallback; }
  catch (e) { return fallback; }
}
function store(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch (e) { /* convenience only */ }
}

function fold(s) { return String(s || '').toLowerCase().replace(/\s+/g, ' ').trim(); }

/* ------------------------------------------------------------- icons */
/* Simple stroked line icons, drawn on a 24-unit grid. */
const ICONS = {
  desk: 'M3 4h18v6H3zM3 14h8v6H3zM15 14h6v6h-6z',
  leaf: 'M3 6h18v12H3zM6 10h8M6 14h12M16 9.5h2',
  tray: 'M4 7l8-4 8 4-8 4zM4 12l8 4 8-4M4 17l8 4 8-4',
  register: 'M5 3h12a2 2 0 0 1 2 2v16H7a2 2 0 0 1-2-2zM5 17a2 2 0 0 1 2-2h12M9 7h6M9 11h6',
  year: 'M12 3a9 9 0 1 0 9 9h-9zM15 3.5A9 9 0 0 1 20.5 9H15z',
  printer: 'M7 9V3h10v6M7 17H4v-7h16v7h-3M7 14h10v7H7z',
  search: 'M10.5 4a6.5 6.5 0 1 1 0 13 6.5 6.5 0 0 1 0-13zM15.5 15.5L21 21',
  check: 'M4 12.5l5 5L20 6.5',
  x: 'M6 6l12 12M18 6L6 18',
  plus: 'M12 5v14M5 12h14',
  bell: 'M6 16V11a6 6 0 0 1 12 0v5l2 2H4zM10 20a2 2 0 0 0 4 0',
  chevrons: 'M8 9l4-4 4 4M8 15l4 4 4-4',
  calendar: 'M4 6h16v14H4zM4 10h16M8 3v4M16 3v4',
  pen: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
  layers: 'M12 3l9 5-9 5-9-5zM3 13l9 5 9-5',
  alert: 'M12 3l10 18H2zM12 10v5M12 18v.5',
  book: 'M4 4h7a3 3 0 0 1 3 3v13a2 2 0 0 0-2-2H4zM20 4h-6M20 4v14h-6',
  clock: 'M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18zM12 7v5l3 2',
  edit: 'M4 20h4L19 9l-4-4L4 16z',
  doc: 'M6 3h9l4 4v14H6zM14 3v5h5',
  undo: 'M9 14L4 9l5-5M4 9h10a6 6 0 0 1 0 12h-3',
};
function icon(name, cls) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('class', 'svg-ico' + (cls ? ' ' + cls : ''));
  const p = document.createElementNS(ns, 'path');
  p.setAttribute('d', ICONS[name] || '');
  svg.appendChild(p);
  return svg;
}
function hydrateIcons(root = document) {
  $$('[data-ico]', root).forEach(n => { if (!n.firstChild) n.appendChild(icon(n.dataset.ico)); });
}

/* ------------------------------------------------------------- state */
/* What the page knows. The server is the record; this is a cache of it that
   every place reads from and re-renders on. */
const State = {
  cal: null, calText: '', photo: false, health: null,
  desk: null, entities: [],
  tray: [],                // drafts not yet in a batch, kept in this browser
  dirty: false,            // something typed that a reload would lose
  printer: '',
};
const TRAY_KEY = 'banyan.batch.drafts';    // kept so drafts already saved still load

const listeners = {};
function on(topic, fn) { (listeners[topic] = listeners[topic] || []).push(fn); }
function emit(topic, data) { (listeners[topic] || []).forEach(fn => { try { fn(data); } catch (e) { console.error(e); } }); }

async function loadDesk() {
  try {
    const desk = await api('/api/desk');
    // Without cheque books, the next leaf is the register's own guess.
    if (!desk.books.next_leaf) {
      try { desk.suggested_leaf = (await api('/api/next-cheque-no')).cheque_no || ''; } catch (e) { desk.suggested_leaf = ''; }
    }
    // The desk is polled; an answer that says nothing new must not redraw the
    // page under the person's hands.
    const text = JSON.stringify(desk);
    const same = text === State.deskText;
    State.deskText = text;
    State.desk = desk;
    State.entities = desk.entities;
    if (same) return;
  } catch (err) {
    toast(err.message, true);
    return;
  }
  emit('desk');
}

function saveTray() { store(TRAY_KEY, State.tray); emit('tray'); }

function entityById(id) { return State.entities.find(e => e.id === id) || null; }
function entityLabel(e) { return e ? (e.entity_name || e.payee_name) : ''; }
function rhythmOf(id) {
  return (State.desk && State.desk.rhythms.find(r => r.entity_id === id)) || null;
}
/* The usual amount: the rhythm's median, else the average so far. */
function usualPaise(e) {
  const r = rhythmOf(e.id);
  if (r) return r.typical_paise;
  return e.count ? Math.round(e.total_paise / e.count) : 0;
}
function entitiesPrinting(payee) {
  const name = String(payee || '').split(/\s+/).filter(Boolean).join(' ');
  return name ? State.entities.filter(e => e.payee_name === name) : [];
}
function defaultMemo(e, date) {
  const d = parseISO(date || todayISO());
  return [e.category, `${MONTHS[d.getMonth()]} ${d.getFullYear()}`].filter(Boolean).join(' — ');
}

/* The leaf the next cheque goes on: the book in use, if any are set up. */
function nextLeaf() {
  const d = State.desk;
  return d ? (d.books.next_leaf || d.suggested_leaf || '') : '';
}
async function suggestLeaf() {
  if (nextLeaf()) return nextLeaf();
  try { return (await api('/api/next-cheque-no')).cheque_no || ''; } catch (e) { return ''; }
}

/* A draft for the tray from an entity: its usual amount and its date rule. */
function draftFor(e, overrides = {}) {
  const date = e.rule_date || todayISO();
  const paise = usualPaise(e);
  return Object.assign({
    // A payee-per-cheque entity starts from its prefix; the rest is typed.
    payee: e.kind === 'per_cheque' ? (e.payee_prefix ? e.payee_prefix + ' ' : '') : e.payee_name, entity_id: e.id,
    amount: paise ? plainAmount(paise) : '', amount_paise: paise,
    cheque_date: date, memo: defaultMemo(e, date),
    crossing: e.ac_payee !== false, strike_order: false, star_fill: false, one_off: false,
  }, overrides);
}

/* ------------------------------------------------------------- dialogs */
/* A modal built from nodes. Resolves with the value of the button pressed,
   or '' when dismissed. Esc never confirms anything. */
function modal({ title, eyebrow, body, actions = [], wide, className, onOpen }) {
  return new Promise(resolve => {
    const dlg = h('dialog.modal' + (wide ? '.modal-wide' : '') + (className ? '.' + className : ''));
    const form = h('form', { method: 'dialog' });
    if (eyebrow) form.appendChild(h('div.eyebrow', eyebrow));
    if (title) form.appendChild(h('h2.modal-title', title));
    append(form, [body]);
    const bar = h('menu.modal-actions');
    actions.forEach(a => {
      const b = h('button.btn' + (a.primary ? '.btn-primary' : a.danger ? '.btn-danger' : '.btn-quiet'),
                  { value: a.value || 'ok', type: a.submit === false ? 'button' : 'submit' }, a.label);
      if (a.disabled) b.disabled = true;
      if (a.onClick) b.addEventListener('click', ev => a.onClick(ev, dlg));
      if (a.id) b.id = a.id;
      bar.appendChild(b);
    });
    form.appendChild(bar);
    dlg.appendChild(form);
    dlg.returnValue = '';
    dlg.addEventListener('close', () => { resolve(dlg.returnValue || ''); dlg.remove(); });
    dlg.addEventListener('cancel', () => { dlg.returnValue = ''; });
    document.body.appendChild(dlg);
    hydrateIcons(dlg);
    dlg.showModal();
    if (onOpen) onOpen(dlg);
  });
}

function dl(rows) {
  return h('dl.facts', rows.filter(Boolean).map(([k, v, cls]) =>
    h('div' + (cls ? '.' + cls : ''), h('dt', k), h('dd', v))));
}

/* The loading instruction, said the same way everywhere a real leaf is
   about to be printed. */
function loadingNote(text) {
  return h('div.load-note', icon('alert'),
    h('p', text || 'Load the leaf face up, sideways, with the Pay end going in first. ' +
      'Slide both width guides against its long edges.'));
}

/* ------------------------------------------------------------- health */
function printerOf(hh) {
  const ps = (hh && hh.printing.printers) || [];
  return ps.find(p => p.name === State.printer) || ps.find(p => p.is_default) || ps[0] || null;
}

async function loadHealth() {
  try {
    State.health = await api('/api/health');
  } catch (err) {
    State.health = null;
  }
  const hh = State.health;
  const p = printerOf(hh);
  const ok = hh && hh.renderer.ok && !hh.calibration.error && p && p.online !== false;
  $('#printerDot').className = 'dot ' + (ok ? 'ok' : 'bad');
  $('#printerText').textContent = !hh ? 'server unreachable'
    : !p ? 'no printer found'
    : `${p.name.replace(/_/g, ' ').replace(/^HP LaserJet Pro MFP /, '')} · ${p.online === false ? 'offline' : 'ready'}`;
  emit('health');
}

/* The calibration is re-read whenever it changes, so every leaf on the page
   re-lays itself — ink and inputs together — without a reload. */
async function loadCalibration() {
  let text;
  try {
    const cal = await api('/api/calibration');
    text = JSON.stringify(cal);
    if (text === State.calText) return;
    const first = !State.cal;
    State.cal = cal;
    State.calText = text;
    if (!first) toast(`Calibration v${cal.version} loaded — every leaf has been re-laid.`);
    emit('cal');
  } catch (err) {
    if (!State.cal) toast('Could not load the cheque calibration: ' + err.message, true);
  }
}

function healthBlocksPrint() {
  const hh = State.health;
  if (!hh) return 'The server is not answering.';
  if (hh.calibration.error) return `The calibration file is broken: ${hh.calibration.error}`;
  if (!hh.renderer.ok) return `The renderer is not ready: ${hh.renderer.path}`;
  const p = printerOf(hh);
  if (!p) return 'No printer was found.';
  if (p.online === false) return `${p.name.replace(/_/g, ' ')} is offline.`;
  return '';
}

/* ------------------------------------------------------------- router */
const Places = {};       // name -> { title, render(params), leave?() }
const Router = { place: '', params: {} };

function parseHash() {
  const raw = (location.hash || '#/desk').slice(2);
  const [path, query] = raw.split('?');
  const params = Object.fromEntries(new URLSearchParams(query || ''));
  const [place, sub] = path.split('/');
  if (sub) params.sub = sub;
  // The Leaf page became the Tray's preview; old #/leaf links land on the Tray.
  if (place === 'leaf') return { place: 'tray', params };
  return { place: Places[place] ? place : 'desk', params };
}

function go(place, params = {}) {
  const sub = params.sub ? '/' + params.sub : '';
  const rest = Object.assign({}, params);
  delete rest.sub;
  const q = new URLSearchParams(Object.entries(rest).filter(([, v]) => v != null && v !== '')).toString();
  const hash = `#/${place}${sub}${q ? '?' + q : ''}`;
  if (location.hash === hash) route(); else location.hash = hash;
}

function route() {
  const { place, params } = parseHash();
  const prev = Places[Router.place];
  if (prev && prev.leave) prev.leave();
  Router.place = place;
  Router.params = params;
  $$('.rail-item[data-place]').forEach(a => {
    const onIt = a.dataset.place === place;
    a.classList.toggle('is-active', onIt);
    if (onIt) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  });
  $('#placeTitle').textContent = Places[place].title;
  document.title = `${Places[place].title} · Cheque Desk`;
  const content = $('#content');
  // Redrawing the place already on screen keeps the reader where they were;
  // only arriving somewhere starts at the top.
  const stay = Router.hash === location.hash ? content.scrollTop : 0;
  Router.hash = location.hash;
  content.replaceChildren();
  content.className = 'content place-' + place;
  Places[place].render(content, params);
  hydrateIcons(content);
  updateDock();
  content.scrollTop = stay;
}

/* Re-render the current place when data it shows changes, unless the
   person is typing in it. */
function rerender() {
  const p = Places[Router.place];
  if (!p) return;
  if (p.refresh) { p.refresh(); return; }
  const a = document.activeElement;
  if (a && $('#content').contains(a) && /INPUT|TEXTAREA|SELECT/.test(a.tagName)) return;
  route();
}

function updateDock() { emit('dock'); }

/* Element.append writes null as the text "null"; this skips it. */
function appendTo(n, ...kids) { return append(n, kids); }
