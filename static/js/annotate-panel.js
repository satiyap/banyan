/* The one place a category (a report head, in the code) or a note is changed.
 *
 * Opened from the reports (a cheque or an entity in Year or Monthly) and
 * from the desk's review list. Reading stays read-only: nothing changes
 * until Save, the panel says first what the change will move, and the
 * confirmation offers Undo. Anyone with a key may do this; the latest change
 * to reach the server wins, and each change shows who made it.
 */
'use strict';

const NOTE_MAX = 500;

/* A head's colour: the report's own palette when the caller has one, else a
   fixed one by the order of the heads, so a head reads the same everywhere. */
function annotateColour(ctx, head) {
  if (ctx && ctx.colour) return ctx.colour(head);
  const at = (ctx && ctx.heads || []).indexOf(head);
  return at >= 0 && typeof HEAD_COLOURS !== 'undefined' ? HEAD_COLOURS[at] : (typeof OTHER_COLOUR !== 'undefined' ? OTHER_COLOUR : '#9aa3a8');
}

function changedLine(what, e) {
  if (!e) return null;
  return h('div', `${what} by ${e.mine ? 'this desk' : (e.by || 'someone')} · ${fmtStamp(e.at)}`);
}

/* How a cheque's head reads in a list: an inherited one quietly, its own one
   as a chip in the head's colour, so the exceptions stand out. */
function headTag(leaf, ctx) {
  if (leaf.own_head) {
    return h('span.ann-chip', { title: `Set on this cheque. ${leaf.entity_name}'s category is ${leaf.entity_head}.` },
      h('i', { style: { background: annotateColour(ctx, leaf.own_head) } }), leaf.own_head, h('span.ann-own', 'own category'));
  }
  return h('span.ann-inherit', { title: `${leaf.entity_name}'s category` }, leaf.head);
}

/* The cheques under one row of a report, each opening the panel. */
function leafDrill(leaves, ctx, oneEntity) {
  if (!leaves.length) return h('p.empty', 'No cheques here.');
  return h('ul.ann-drill' + (oneEntity ? '.is-one-entity' : ''), leaves.map(l => h('li',
    h('button.ann-leaf', { type: 'button', onclick: () => Annotate.leaf(l, ctx),
        'aria-label': `Cheque ${l.cheque_no || l.id}, ${l.entity_name}, ${rupees(l.amount_paise, { always: true })}. Change category or note` },
      h('span.mono', l.cheque_no || '—'),
      h('span.sub', fmtDay(l.cheque_date)),
      h('span.ann-leaf-name', l.entity_name),
      headTag(l, ctx),
      h('span.mono.num', rupees(l.amount_paise, { always: true })),
      l.notes ? h('span.ann-leaf-note', h('span.ico', { 'data-ico': 'doc', 'aria-hidden': 'true' }), l.notes) : h('span'))))) ;
}

/* A note marker for a row: the icon, with the text for anyone who looks. */
function noteMark(text) {
  if (!text) return null;
  return h('span.ann-mark', { title: text, 'aria-label': `Note: ${text}` }, h('span.ico', { 'data-ico': 'doc', 'aria-hidden': 'true' }));
}

/* Confirmation with Undo, in the page's own toast. */
function toastUndo(message, undo) {
  const t = $('#toast');
  const btn = h('button.btn.btn-quiet.btn-xs.ann-undo', { type: 'button' }, 'Undo');
  btn.addEventListener('click', () => {
    t.classList.remove('show');
    undo();
  });
  clear(t, h('span', message), btn);
  t.classList.remove('bad');
  t.classList.add('show', 'has-action');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show', 'has-action'), 8000);
}

async function saveFields(subject, changes) {
  for (const [field, value] of changes) await post('/api/annotations', { subject, field, value });
}

const Annotate = {
  dialog: null,

  shell() {
    if (this.dialog) return this.dialog;
    const d = h('dialog.ann-panel', { 'aria-labelledby': 'annTitle' });
    d.addEventListener('click', ev => { if (ev.target === d) d.close(); });
    d.addEventListener('close', () => { if (this.opener && this.opener.isConnected) this.opener.focus(); });
    document.body.appendChild(d);
    this.dialog = d;
    return d;
  },

  /* ctx: { heads, colour?, leaves? (the period's cheques), label?, onSaved() } */
  leaf(leaf, ctx) {
    const entity = (ctx.entities || []).find(e => e.id === leaf.entity_id);
    this.open({
      ctx, subject: `cheque:${leaf.id}`, title: leaf.entity_name,
      facts: [`Cheque ${leaf.cheque_no || '—'}`, fmtDay(leaf.cheque_date), rupees(leaf.amount_paise, { always: true })],
      payee: leaf.payee !== leaf.entity_name ? `Paid to ${leaf.payee}` : '',
      head: leaf.own_head, note: leaf.notes,
      none: `Same as ${leaf.entity_name} (${leaf.entity_head})`,
      headEdited: leaf.head_edited, noteEdited: leaf.notes_edited,
      effect(head) {
        const now = leaf.head, next = head || leaf.entity_head;
        if (now === next) return '';
        return `${rupees(leaf.amount_paise, { whole: true })} moves from ${now} to ${next} in ${monthOf(leaf.cheque_date)}.`;
      },
      also: entity ? { text: `Change ${entity.name}'s category for all its cheques instead`, go: () => Annotate.entity(entity, ctx) } : null,
      register: leaf.cheque_no && Places.register ? `#/register?leaf=${encodeURIComponent(leaf.cheque_no)}` : null,
    });
  },

  entity(entity, ctx) {
    const mine = (ctx.leaves || []).filter(l => l.entity_id === entity.id);
    const following = mine.filter(l => !l.own_head);
    const fallback = entity.kind === 'one_off' ? 'Misc' : 'Uncategorised';
    this.open({
      ctx, subject: `entity:${entity.id}`, title: entity.name,
      facts: [plural(entity.count, 'cheque'), rupees(entity.paise, { whole: true }) + (ctx.label ? ` in ${ctx.label}` : '')],
      payee: entity.payee_name !== entity.name ? `Pays ${entity.payee_name}` : '',
      head: entity.head_set, note: entity.notes,
      none: `None chosen (${fallback})`,
      headEdited: entity.head_edited, noteEdited: entity.notes_edited,
      effect(head) {
        const now = entity.head, next = head || fallback;
        if (now === next || !following.length) return '';
        const paise = following.reduce((s, l) => s + l.amount_paise, 0);
        const kept = mine.length - following.length;
        return `${plural(following.length, 'cheque')}, ${rupees(paise, { whole: true })}, move from ${now} to ${next}${ctx.label ? ` in ${ctx.label}` : ''}.` +
          (kept ? ` ${plural(kept, 'cheque')} with a category of ${kept === 1 ? 'its' : 'their'} own stay${kept === 1 ? 's' : ''} where ${kept === 1 ? 'it is' : 'they are'}.` : '');
      },
    });
  },

  open(o) {
    const d = this.shell();
    this.opener = document.activeElement;
    const heads = o.ctx.heads || [];
    const name = 'annHead' + Math.random().toString(36).slice(2, 7);
    const options = [['', o.none], ...heads.map(x => [x, x])];
    const radios = options.map(([value, text]) => {
      const input = h('input', { type: 'radio', name, value, checked: value === (o.head || '') });
      return h('label.ann-option', input,
        value ? h('i', { style: { background: annotateColour(o.ctx, value) } }) : h('i.ann-none'),
        h('span', text));
    });
    const note = h('textarea.ann-note-field', { id: 'annNote', rows: 3, maxLength: NOTE_MAX, 'aria-describedby': 'annNoteCount annNoteEdited' });
    note.value = o.note || '';
    const count = h('span.sub', { id: 'annNoteCount' });
    const effect = h('p.ann-effect', { 'aria-live': 'polite' });
    const error = h('p.ann-error', { role: 'alert', hidden: true });
    const save = h('button.btn.btn-primary', { type: 'submit', disabled: true }, 'Save');
    const picked = () => (d.querySelector(`input[name="${name}"]:checked`) || {}).value || '';
    const changes = () => {
      const out = [];
      if (picked() !== (o.head || '')) out.push(['head', picked()]);
      if (note.value.trim() !== (o.note || '').trim()) out.push(['notes', note.value.trim()]);
      return out;
    };
    const update = () => {
      count.textContent = `${note.value.length} / ${NOTE_MAX}`;
      const text = changes().some(c => c[0] === 'head') ? o.effect(picked()) : '';
      effect.textContent = text;
      effect.hidden = !text;
      save.disabled = !changes().length;
    };
    const form = h('form.ann-form', { method: 'dialog' },
      h('header.ann-head',
        h('div',
          h('h2#annTitle', o.title),
          h('p.ann-facts', o.facts.join(' · ')),
          o.payee ? h('p.sub', o.payee) : null),
        h('button.btn.btn-quiet.btn-sm.ann-close', { type: 'button', 'aria-label': 'Close without saving', onclick: () => d.close() }, icon('x'))),
      h('fieldset.ann-heads', { 'aria-describedby': 'annHeadEdited' },
        h('legend', 'Category'),
        h('div.ann-options', radios),
        h('div.sub.ann-edited', { id: 'annHeadEdited' }, changedLine('Category set', o.headEdited))),
      effect,
      o.also ? h('button.btn.btn-quiet.btn-xs.ann-also', { type: 'button', onclick: () => { d.close(); o.also.go(); } }, o.also.text) : null,
      h('div.ann-note',
        h('label', { for: 'annNote' }, 'Note'),
        note,
        h('div.ann-note-foot', h('div.sub', { id: 'annNoteEdited' }, changedLine('Note written', o.noteEdited)), count)),
      error,
      h('footer.ann-foot',
        o.register ? h('a.btn.btn-quiet.btn-sm', { href: o.register, onclick: () => d.close() }, 'Open in Register') : h('span'),
        h('div.ann-actions',
          h('button.btn.btn-quiet', { type: 'button', onclick: () => d.close() }, 'Cancel'),
          save)));
    form.addEventListener('input', update);
    form.addEventListener('change', update);
    form.addEventListener('submit', async ev => {
      ev.preventDefault();
      const todo = changes();
      if (!todo.length) return;
      const before = todo.map(([field]) => [field, field === 'head' ? (o.head || '') : (o.note || '')]);
      save.disabled = true;
      save.textContent = 'Saving…';
      error.hidden = true;
      try {
        await saveFields(o.subject, todo);
      } catch (err) {
        // Nothing on the page changed: the panel keeps what was typed, to try again.
        error.textContent = `Not saved: ${err.message}`;
        error.hidden = false;
        save.textContent = 'Save';
        save.disabled = false;
        return;
      }
      const label = this.opener && this.opener.getAttribute && this.opener.getAttribute('aria-label');
      d.close();
      // The report redraws with the change; focus goes back to the same row
      // once it is there again, so a keyboard user keeps their place.
      const done = () => { if (o.ctx.onSaved) o.ctx.onSaved(); if (label) refocus(label); };
      done();
      toastUndo(todo.length > 1 ? 'Category and note saved.' : todo[0][0] === 'head' ? 'Category saved.' : 'Note saved.', async () => {
        try { await saveFields(o.subject, before); toast('Put back as it was.'); done(); }
        catch (err) { toast(`Could not undo: ${err.message}`, true); }
      });
    });
    clear(d, form);
    hydrateIcons(d);
    update();
    d.showModal();
    (d.querySelector(`input[name="${name}"]:checked`) || note).focus();
  },
};

function refocus(label, tries = 20) {
  const el = $$('[aria-label]').find(n => n.getAttribute('aria-label') === label);
  if (el) { el.focus(); return; }
  if (tries) setTimeout(() => refocus(label, tries - 1), 150);
}

function monthOf(iso) {
  const d = parseISO(iso);
  return `${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}
