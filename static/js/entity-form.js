/* An entity: who the association pays.
 *
 * Of everything here only the payee name is ever printed — on the Pay line,
 * exactly as the bank account reads. A payee-per-cheque entity (GST, paid to
 * a fresh CRN each time) prints no fixed name: its payee is typed on each
 * cheque in the Tray, starting from an optional prefix kept here. The rest is the desk's own memory of how
 * this entity is paid: what it is called, its head, whether it is a one-off,
 * how its cheques are dated, and when to be reminded about post-dated ones.
 */
'use strict';

const RULE_RE = /^(today|(next|this):([1-9]|[12]\d|3[01]))$/;

function ordinal(n) {
  const s = ['th', 'st', 'nd', 'rd'], v = n % 100;
  return n + (s[(v - 20) % 10] || s[v] || s[0]);
}

function ruleText(rule) {
  const m = RULE_RE.exec(rule || 'today');
  if (!m || m[1] === 'today') return 'dated the day it is written';
  return `post-dated to the ${ordinal(Number(m[3]))} of ${m[2] === 'next' ? 'next month' : 'the coming month'}`;
}

/* The date a cheque written today would carry, as the server works it out
   (dating.rule_date) — kept to the same rule so a preview never disagrees. */
function ruleDate(rule, today) {
  const m = RULE_RE.exec(rule || 'today');
  const t = parseISO(today || todayISO());
  if (!m || m[1] === 'today') return iso(t);
  const day = Number(m[3]);
  const onDay = (y, mo) => new Date(y, mo, Math.min(day, new Date(y, mo + 1, 0).getDate()));
  const next = onDay(t.getFullYear(), t.getMonth() + 1);
  if (m[2] === 'next') return iso(next);
  const thisMonth = onDay(t.getFullYear(), t.getMonth());
  return iso(thisMonth > t ? thisMonth : next);
}

function seg(name, options, value, onChange) {
  const box = h('div.seg', { role: 'radiogroup', 'aria-label': name });
  options.forEach(([v, label]) => {
    const b = h('button', { type: 'button', role: 'radio', 'aria-checked': String(v === value) }, label);
    b.addEventListener('click', () => {
      $$('button', box).forEach(x => x.setAttribute('aria-checked', String(x === b)));
      onChange(v);
    });
    box.appendChild(b);
  });
  return box;
}

function field(label, control, note, opts = {}) {
  const id = control.id || ('f' + Math.random().toString(36).slice(2, 8));
  if (control.matches && control.matches('input,select,textarea')) control.id = id;
  return h('div.field' + (opts.cls ? '.' + opts.cls : ''),
    h('label', { for: id }, label),
    control,
    note ? (note instanceof Node ? note : h('p.field-note', note)) : null);
}

/* The form, for a new entity or an existing one. `prefill` seeds a new one
   (the Tray passes what was typed in the row). `withAmount` adds an amount
   field whose value fills the row; it is not saved on the entity. */
function entityForm(entity, prefill = {}, opts = {}) {
  const e = Object.assign({ entity_name: '', payee_name: '', category: '', notes: '',
                            kind: 'regular', ac_payee: true, seal_required: false,
                            date_rule: 'today', remind_days: '3,1,0', payee_prefix: '' }, entity || {}, prefill);
  const v = Object.assign({}, e);

  const name = h('input', { type: 'text', value: e.entity_name, placeholder: 'e.g. Security agency',
                            maxlength: '100', autocomplete: 'off' });
  // One box, two meanings: the fixed printed name, or a per-cheque prefix.
  const kept = { fixed: e.payee_name, prefix: e.payee_prefix || '' };
  const perCheque = () => v.kind === 'per_cheque';
  const payee = h('input', { type: 'text', value: perCheque() ? kept.prefix : kept.fixed, autocomplete: 'off' });
  const payeeLabel = h('label');
  const fitNote = h('p.field-note');
  function paintPayeeField() {
    payee.placeholder = perCheque() ? 'e.g. YOURSELF CRN — or leave empty' : 'Exactly as on the bank account';
    payee.maxLength = perCheque() ? 60 : 120;
    payee.required = !perCheque();
    payeeLabel.textContent = perCheque() ? 'Payee prefix · prefilled in the Tray (optional)' : 'Payee · printed on the leaf';
  }
  const head = h('input', { type: 'text', value: e.category, placeholder: 'e.g. maintenance',
                            maxlength: '60', list: 'headList', autocomplete: 'off' });
  const reportHead = h('select', { 'aria-label': 'Report head' },
    h('option', { value: '', selected: !e.head }, 'Not chosen'),
    ['Salary', 'Utilities', 'AMC', 'Misc', 'GST / Tax'].map(x => h('option', { value: x, selected: e.head === x }, x)));
  const heads = h('datalist', { id: 'headList' },
    [...new Set(State.entities.map(x => x.category).filter(Boolean))].sort().map(c => h('option', { value: c })));
  const notes = h('input', { type: 'text', value: e.notes, placeholder: 'Anything worth remembering',
                             maxlength: '500' });
  const amount = opts.withAmount ? h('input', { type: 'text', inputmode: 'decimal',
                                                 value: prefill.amount || '', placeholder: '0.00' }) : null;
  const acPayee = h('input', { type: 'checkbox', checked: e.ac_payee !== false });
  const seal = h('input', { type: 'checkbox', checked: !!e.seal_required });
  const oneOffNote = h('p.field-note');
  const ruleBox = h('div.rule-box');
  const remindBox = h('div.remind-box');
  const error = h('p.form-error', { hidden: true });

  function checkFit() {
    const text = payee.value.split(/\s+/).filter(Boolean).join(' ');
    fitNote.classList.remove('bad');
    if (perCheque()) {
      fitNote.textContent = (text ? `Each new ${name.value.trim() || 'cheque'} row starts with “${text} ”; type the rest after it, or overwrite it for one cheque.`
        : 'The payee is typed on each cheque in the Tray.') + (entity && entity.payees && entity.payees.length ? ` Its earlier payees are offered too (${entity.payees.length} so far).` : '');
      return;
    }
    const others = State.entities.filter(x => x.payee_name === text && (!entity || x.id !== entity.id));
    const bits = [];
    if (text && State.cal) {
      const fit = ChequeLayout.fitText(text, State.cal.fields.payee);
      bits.push(fit.overflow ? 'Too long for the PAY line even at the smallest legible size — shorten it.'
        : fit.size < State.cal.fields.payee.size ? `Fits the PAY line at ${fit.size}mm (shrunk from ${State.cal.fields.payee.size}mm).`
        : `Fits the PAY line at full size (cal v${State.cal.version}).`);
      fitNote.classList.toggle('bad', fit.overflow);
    }
    if (others.length) {
      bits.push(`${plural(others.length, 'other entity', 'other entities')} already ${others.length === 1 ? 'pays' : 'pay'} this name — allowed; the entity name tells them apart.`);
    }
    fitNote.textContent = bits.join(' ') || 'The only field that reaches the cheque.';
  }
  payee.addEventListener('input', checkFit);
  checkFit();

  function renderKindNote() {
    oneOffNote.textContent = {
      one_off: 'One-off: kept out of the rhythm, grouped under One-off in reports, no due date on the Desk.',
      per_cheque: 'Payee per cheque: counts in the rhythm like a regular entity, but each cheque prints the payee typed for it — GST to a fresh CRN, say. The register keeps exactly what was printed.',
    }[v.kind] || 'Regular: its rhythm and next due date are worked out from the cheques written to it.';
  }
  function setKind(k) {
    if (perCheque()) kept.prefix = payee.value; else kept.fixed = payee.value;
    v.kind = k;
    payee.value = perCheque() ? kept.prefix : kept.fixed;
    paintPayeeField(); checkFit(); renderKindNote();
  }
  paintPayeeField();
  renderKindNote();

  let custom = !['today', 'next:1'].includes(v.date_rule);
  function renderRule() {
    const m = RULE_RE.exec(v.date_rule) || [];
    const post = m[1] && m[1] !== 'today';
    const day = h('select', { 'aria-label': 'Day of the month' },
      Array.from({ length: 31 }, (_, i) => h('option', { value: String(i + 1), selected: post && Number(m[3]) === i + 1 }, ordinal(i + 1))));
    const which = h('select', { 'aria-label': 'Which month' },
      h('option', { value: 'next', selected: !post || m[2] === 'next' }, 'next month'),
      h('option', { value: 'this', selected: post && m[2] === 'this' }, 'the coming month'));
    const set = () => { v.date_rule = `${which.value}:${day.value}`; renderPreview(); renderRemind(); };
    day.addEventListener('change', set);
    which.addEventListener('change', set);
    const preview = h('p.field-note');
    function renderPreview() {
      const t = todayISO();
      const d = ruleDate(v.date_rule, t);
      preview.textContent = v.date_rule === 'today'
        ? 'Normal for bills: the cheque carries the day it is written.'
        : `A cheque written today (${fmtShort(t)}) prints dated ${fmtDay(d)} and is marked POST-DATED in the register. You can still change the date on any single leaf before printing.`;
    }
    clear(ruleBox,
      seg('Date rule', [['today', 'Today'], ['next:1', '1st next month'], ['custom', 'Day…']],
        custom ? 'custom' : v.date_rule,
        val => {
          custom = val === 'custom';
          if (custom) { if (!post) v.date_rule = 'next:1'; } else v.date_rule = val;
          renderRule(); renderRemind();
        }),
      custom ? h('div.rule-custom', 'Post-date to the ', day, ' of ', which) : null,
      preview);
    renderPreview();
  }

  function renderRemind() {
    const post = v.date_rule !== 'today';
    remindBox.hidden = !post;
    if (!post) return;
    const days = new Set(String(v.remind_days || '').split(',').filter(Boolean).map(Number));
    const box = h('div.chips');
    [[7, '7 days'], [3, '3 days'], [1, '1 day'], [0, 'On the day']].forEach(([d, label]) => {
      const b = h('button.chip', { type: 'button', 'aria-pressed': String(days.has(d)) }, label);
      b.addEventListener('click', () => {
        if (days.has(d)) days.delete(d); else days.add(d);
        b.setAttribute('aria-pressed', String(days.has(d)));
        v.remind_days = [...days].sort((a, b2) => b2 - a).join(',');
      });
      box.appendChild(b);
    });
    clear(remindBox, h('div.label', 'Remind me before the cheque date'), box,
      h('p.field-note', 'Reminders pop up when the desk opens; answering one writes an event on the cheque.'));
  }
  renderRule();
  renderRemind();

  const el = h('div.entity-form',
    field('Entity name · what the desk calls it', name,
          prefill.entity_name && !entity ? 'Started from what you typed in the row.' : null),
    h('div.field', h('div.label', 'Kind'),
      seg('Kind', [['regular', 'Regular'], ['one_off', 'One-off'], ['per_cheque', 'Payee per cheque']], v.kind, setKind),
      oneOffNote),
    payeeField(),
    h('div.field-row',
      field('Report head', reportHead),
      field('Category · a note', head),
      amount ? field('Amount', h('div.amount-input', h('span', '₹'), amount)) : null),
    heads,
    h('div.field', h('div.label', 'Date rule'), ruleBox),
    remindBox,
    h('label.switch', acPayee, h('span', 'A/C payee crossing on by default')),
    h('label.switch', seal, h('span', 'Needs the seal after signing')),
    field('Notes · never printed', notes),
    error);

  function payeeField() {
    payee.id = 'f' + Math.random().toString(36).slice(2, 8);
    payeeLabel.htmlFor = payee.id;
    return h('div.field', payeeLabel, payee, fitNote);
  }

  return {
    el,
    focus() { (entity ? payee : name).focus(); },
    showError(msg) { error.textContent = msg; error.hidden = !msg; },
    amount: () => (amount ? amount.value.trim() : ''),
    values() {
      const typed = payee.value.split(/\s+/).filter(Boolean).join(' ');
      if (perCheque()) kept.prefix = typed; else kept.fixed = typed;
      // A payee-per-cheque entity's payee_name is only a label (never printed,
      // never matched): keep the one it has, or use its entity name.
      const label = name.value.trim();
      return {
        entity_name: label,
        payee_name: perCheque() ? (entity && entity.payee_name) || label || kept.prefix : kept.fixed,
        payee_prefix: perCheque() ? kept.prefix : (entity ? entity.payee_prefix || '' : ''),
        category: head.value.trim(), head: reportHead.value, notes: notes.value.trim(), kind: v.kind,
        ac_payee: acPayee.checked, seal_required: seal.checked,
        date_rule: v.date_rule, remind_days: v.remind_days || '',
      };
    },
  };
}

/* Save a form's values: a new entity, or changes to an existing one.
   Resolves with the saved entity as the server now has it. */
async function saveEntity(entity, values) {
  const res = entity
    ? await api(`/api/entities/${entity.id}`, { method: 'PUT', body: values })
    : await post('/api/entities', values);
  await loadDesk();
  return entityById(entity ? entity.id : res.id);
}

async function editEntity(entity, prefill) {
  const form = entityForm(entity, prefill);
  let saved = null;
  const unpaid = entity && !entity.count;
  const choice = await modal({
    eyebrow: entity ? `Entity · ${entityLabel(entity)}` : 'New entity',
    title: entity ? 'Edit entity' : 'Add an entity',
    body: form.el, wide: true,
    onOpen: () => form.focus(),
    actions: [
      unpaid ? { label: 'Remove', danger: true, submit: false, value: 'remove',
                 onClick: async (ev, dlg) => {
                   ev.preventDefault();
                   try {
                     await api(`/api/entities/${entity.id}`, { method: 'DELETE' });
                     await loadDesk();
                     toast('Entity removed.');
                     dlg.close('removed');
                   } catch (err) { form.showError(err.message); }
                 } } : null,
      { label: 'Cancel', value: 'cancel' },
      { label: entity ? 'Save' : 'Add entity', primary: true, value: 'save',
        onClick: async (ev, dlg) => {
          ev.preventDefault();
          try {
            saved = await saveEntity(entity, form.values());
            dlg.close('save');
          } catch (err) { form.showError(err.message); }
        } },
    ].filter(Boolean),
  });
  if (choice === 'save' && saved) toast(entity ? 'Entity saved.' : `${entityLabel(saved)} added.`);
  return choice === 'save' ? saved : null;
}
