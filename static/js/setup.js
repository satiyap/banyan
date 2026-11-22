/* Settings: the app's link to the Banyan server.
 *
 * First launch is one sheet in two steps: the API URL, which the app checks
 * is a Banyan server, then the user key, which is redeemed there. After that,
 * Settings is a panel in the app's own shell with three rows. The address is
 * settled and shown as text; the key can be switched or reset on its own;
 * and the register row says how the server's copy stands.
 */
(function () {
  // The words here are written for a Mac. On Windows the desk is "this
  // computer", put right as the words appear, server messages included.
  if (/Windows/.test(navigator.userAgent)) {
    const fix = (node) => {
      if (node.nodeType === Node.TEXT_NODE) {
        const text = node.nodeValue.replace(/\b([Tt])his Mac\b/g, '$1his computer');
        if (text !== node.nodeValue) node.nodeValue = text;
        return;
      }
      node.childNodes.forEach(fix);
    };
    fix(document.body);
    new MutationObserver((changes) => changes.forEach((c) => {
      if (c.type === 'characterData') fix(c.target);
      else c.addedNodes.forEach(fix);
    })).observe(document.body, { subtree: true, childList: true, characterData: true });
  }
  const version = (document.querySelector('meta[name="desk-version"]') || {}).content || '';
  const el = (id) => document.getElementById(id);
  let now = null;        // the last answer from /api/connection
  let pending = null;    // { api_url, name }: a server that answered but is not connected yet

  async function call(method, path, body) {
    const res = await fetch(path, {
      method,
      headers: { 'Content-Type': 'application/json', 'X-Desk-Version': version },
      body: body ? JSON.stringify(body) : undefined
    });
    const data = await res.json().catch(() => ({}));
    if (res.status === 409 && data.reload) { location.reload(); throw new Error(data.detail); }
    if (!res.ok) throw new Error(typeof data.detail === 'string' ? data.detail : `The app could not do that (${res.status}).`);
    return data;
  }

  const stamp = (ts) => new Date(ts).toLocaleString('en-IN', { day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
  const first = () => document.body.dataset.mode === 'first';
  const errorBox = () => el(first() ? 'setupError' : 'settingsError');
  function fail(message) {
    const box = errorBox();
    // In Settings, a refusal is shown in the row it belongs to, under the field.
    if (!first()) (document.querySelector('.set-row.is-editing') || el('rowSync').parentNode.parentNode).appendChild(box);
    box.textContent = message;
    box.hidden = false;
  }
  function clearError() { el('setupError').hidden = true; el('settingsError').hidden = true; }
  function busy(on) { for (const b of document.querySelectorAll('button')) b.disabled = on; if (!on && now) gate(); }
  const toApp = () => location.replace('/');
  const isReader = () => now.status === 'connected' && now.role === 'read';

  /* ------------------------------------------------------- first launch */
  function firstUrl() {
    pending = null;
    el('stepKey').hidden = true;
    el('stepUrl').hidden = false;
    el('setupLead').textContent = 'Enter the API URL you were given.';
    el('apiUrl').focus();
  }
  function firstKey(found) {
    pending = found;
    el('stepUrl').hidden = true;
    el('stepKey').hidden = false;
    el('serverFound').textContent = `Found ${found.name || 'a Banyan server'} at ${found.api_url}.`;
    el('setupLead').textContent = 'Enter your user key. A W- key makes this Mac the cheque desk; an R- key shows reports only. A key works once.';
    el('userKey').value = '';
    el('userKey').focus();
  }

  /* ------------------------------------------------- Settings in the app */
  const PLACES = {
    desk: [['desk', 'Desk', 'desk'], ['tray', 'Tray', 'tray'], ['register', 'Register', 'register'], ['year', 'Year', 'year'], ['monthly', 'Monthly', 'calendar']],
    reader: [['year', 'Year', 'year'], ['monthly', 'Monthly', 'calendar']]
  };
  function drawRail() {
    const rail = el('rail');
    rail.querySelectorAll('.rail-item[data-place]').forEach((n) => n.remove());
    const push = rail.querySelector('.rail-push');
    for (const [place, label, ico] of PLACES[isReader() ? 'reader' : 'desk']) {
      rail.insertBefore(h('a.rail-item', { href: `/#/${place}`, 'data-place': place }, h('span.ico', { 'data-ico': ico }), h('span', label)), push);
    }
    hydrateIcons(rail);
    const here = rail.querySelector('.rail-settings');
    if (here) { here.classList.add('is-active'); here.setAttribute('aria-current', 'page'); }
  }

  /* Which buttons make sense right now. */
  function gate() {
    const connected = now.status === 'connected';
    const address = pending || (now.api_url ? { api_url: now.api_url, name: now.server_name } : null);
    el('btnSwitch').disabled = !address;
    el('btnReset').hidden = !connected;
  }

  /* One row is edited at a time. While it is, its own buttons step aside for the field. */
  function edit(row) {
    for (const r of document.querySelectorAll('.set-row')) r.classList.toggle('is-editing', r === row);
    el('formAddress').hidden = row !== el('rowAddress');
    el('formKey').hidden = row !== el('rowKey');
  }
  const closeEdits = () => edit(null);

  function draw() {
    const connected = now.status === 'connected';
    const reader = isReader();
    const sync = now.sync || {};
    drawRail();
    el('btnBack').textContent = reader ? 'Back to the reports' : 'Back to the desk';

    const tag = el('roleTag');
    tag.textContent = !connected ? 'Not connected' : reader ? 'Reports only' : 'Cheque desk';
    tag.className = 'tag' + (connected ? ' tone-ok' : ' tone-muted');

    // Address: settled once a server has answered, and shown as text.
    const address = pending || (now.api_url ? { api_url: now.api_url, name: now.server_name } : null);
    el('addrName').textContent = address ? (address.name || 'Banyan server') : 'No server set';
    el('addrUrl').textContent = pending ? `${pending.api_url}. Not saved until you connect with a key.`
      : address ? address.api_url : 'The app works on its own until you add one.';
    el('btnChange').textContent = address ? 'Change' : 'Add a server';

    // Key: what it allows, and the two things that can be done with it.
    el('keyState').textContent = !connected ? 'No key' : reader ? 'Read key' : 'Write key';
    el('keySub').textContent = !connected ? (address ? 'Enter a key to connect.' : 'Add a server first.')
      : reader ? 'This app shows reports only. A write key would make it the cheque desk.'
      : 'This Mac is the cheque desk. Connecting another desk retires this one.';
    el('btnSwitch').textContent = connected ? 'Switch key' : 'Enter a key';

    // Register: how the server's copy stands.
    const row = el('rowSync');
    const bad = connected && !reader && Boolean(sync.error || sync.retired || sync.choice);
    row.classList.toggle('is-bad', bad);
    el('syncState').textContent = !connected ? 'Kept on this Mac only'
      : reader ? 'Not kept by this app'
      : sync.retired ? 'Not syncing'
      : sync.choice ? 'Not syncing'
      : sync.error ? (sync.synced_at ? `Not synced since ${stamp(sync.synced_at)}` : 'Not synced yet')
      : sync.synced_at ? `Synced ${stamp(sync.synced_at)}`
      : 'Nothing sent yet';
    el('syncSub').textContent = !connected ? 'Nothing is sent anywhere.'
      : reader ? 'Reports are fetched from the server each time.'
      : sync.retired ? 'Another desk has taken over. To take over again, switch to a new write key.'
      : sync.choice ? 'The server holds a register this desk did not start from. Choose below.'
      : sync.error ? sync.error
      : 'Every change is copied to the server as it is made.';

    const aside = (!reader && now.set_aside) || [];
    el('bringBack').hidden = !aside.length;
    if (aside.length) {
      el('bringText').textContent = `When this Mac was switched to a read key on ${stamp(aside[0].at)}, its register was set aside. This Mac’s register has no leaves in it now, so that one can be brought back.`;
    }

    const choice = connected && !reader && sync.choice;
    el('choice').hidden = !choice;
    if (choice) {
      el('choiceText').textContent = `The server holds a register it received on ${stamp(sync.choice.synced_at)} (copy ${sync.choice.revision}). The one on this desk is different, and the app cannot tell which is newer.`;
    }
    gate();
  }

  /* Take a fresh answer and show it. Returns true while a choice is still open. */
  function show(answer) {
    now = answer;
    const mode = now.status === 'unset' ? 'first' : 'app';
    document.body.dataset.mode = mode;
    el('first').hidden = mode !== 'first';
    el('app').hidden = mode !== 'app';
    permissions();
    if (mode === 'first') { if (!pending) firstUrl(); return false; }
    addSettingsLink();
    closeEdits();
    draw();
    return Boolean(now.status === 'connected' && now.role !== 'read' && now.sync && now.sync.choice);
  }

  async function act(work, after) {
    clearError();
    busy(true);
    try { const result = await work(); busy(false); after(result); }
    catch (e) { busy(false); fail(e.message); }
  }

  /* Connect with a key. First ask what it will do, without spending it: a key
     that would set this Mac's register aside waits for a confirmation. */
  function connect(apiUrl, key) {
    act(() => call('POST', '/api/connection/plan', { api_url: apiUrl, user_key: key }), (plan) => {
      if (plan.action === 'connect') { redeem(apiUrl, key); return; }
      confirmKey(plan, () => redeem(apiUrl, key, plan.action));
    });
  }
  function redeem(apiUrl, key, confirmed) {
    const body = { api_url: apiUrl, user_key: key };
    if (confirmed) body.confirm = confirmed;
    act(() => call('PUT', '/api/connection', body), (answer) => {
      pending = null;
      if (confirmed) { restarting(); return; }
      if (!show(answer)) toApp();
    });
  }
  function restarting() {
    const box = confirmBox();
    box.replaceChildren(h('p', 'Done. The app is restarting.'));
    box.hidden = false;
  }

  /* The one question a key can raise, worded the same for both kinds of key. */
  function confirmBox() {
    const form = first() ? el('stepKey') : el('formKey');
    let box = document.getElementById('keyConfirm');
    if (!box) box = h('div.key-confirm', { id: 'keyConfirm', role: 'group', 'aria-labelledby': 'keyConfirmHead' });
    form.after(box);
    return box;
  }
  function confirmKey(plan, go) {
    const local = plan.local || {};
    const leaves = local.leaves || [];
    const server = (plan.server && plan.server.name) || 'The server';
    const listed = leaves.length > 6 ? leaves.slice(0, 6).join(', ') + ` and ${leaves.length - 6} more` : leaves.join(', ');
    const lines = [];
    let head, action;
    if (plan.action === 'replace') {
      head = `${server} already holds a register.`;
      action = 'Set this Mac’s register aside and use the server’s';
      lines.push('This Mac becomes the cheque desk and uses the server’s register. This Mac’s own register is kept as a file, not deleted.');
      if (leaves.length) lines.push(`The ${leaves.length === 1 ? 'leaf' : `${leaves.length} leaves`} this Mac used (${listed}) ${leaves.length === 1 ? 'is' : 'are'} recorded in the server’s register as used, so none is offered again.`);
    } else {
      head = 'This is a read key.';
      action = 'Set this Mac’s register aside and show the server’s reports';
      const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;
      lines.push(`This Mac becomes a reports-only app. Its register (${count(local.entities || 0, 'payee', 'payees')}, ${count(leaves.length, 'leaf', 'leaves')}) is kept as a file, not deleted.`);
      if (local.unsynced) lines.push('These records were never sent to a server. The file on this Mac will be the only copy of them.');
    }
    lines.push('The app restarts.');
    const yes = h('button.btn.btn-primary.btn-sm', { type: 'button' }, action);
    const no = h('button.btn.btn-quiet.btn-sm', { type: 'button' }, 'Cancel');
    // Records that exist nowhere else ask twice.
    const twice = plan.action === 'set-aside' && local.unsynced;
    yes.addEventListener('click', () => {
      if (twice && !yes.dataset.armed) { yes.dataset.armed = '1'; yes.textContent = 'Yes, set them aside'; return; }
      go();
    });
    no.addEventListener('click', () => { box.hidden = true; clearError(); });
    const box = confirmBox();
    box.replaceChildren(
      h('p.key-confirm-head', { id: 'keyConfirmHead' }, head),
      ...lines.map((line, i) => h(i === 1 && twice ? 'p.key-confirm-warn' : 'p', line)),
      h('div.btn-row', yes, no));
    box.hidden = false;
    yes.focus();
  }

  /* A button that discards something asks once more before it acts. */
  function armed(button, sure, work) {
    const label = button.textContent;
    let timer;
    const disarm = () => { clearTimeout(timer); delete button.dataset.armed; button.textContent = label; };
    button.addEventListener('click', () => {
      if (button.dataset.armed) { disarm(); work(); return; }
      button.dataset.armed = '1';
      button.textContent = sure;
      timer = setTimeout(disarm, 6000);
    });
  }

  // ------------------------------------------------- first launch: wiring
  el('stepUrl').addEventListener('submit', (event) => {
    event.preventDefault();
    act(() => call('POST', '/api/connection/check', { api_url: el('apiUrl').value }), firstKey);
  });
  el('stepKey').addEventListener('submit', (event) => {
    event.preventDefault();
    connect(pending.api_url, el('userKey').value);
  });
  el('btnBackToUrl').addEventListener('click', () => { clearError(); firstUrl(); });
  // Skipping is an answer, and is remembered: the question is put once.
  for (const b of document.querySelectorAll('.js-skip')) {
    b.addEventListener('click', () => act(() => call('POST', '/api/connection/skip', { forget_server: true }), toApp));
  }

  // ------------------------------------------------------ Settings: wiring
  el('btnChange').addEventListener('click', () => {
    clearError();
    edit(el('rowAddress'));
    el('setAddress').value = (pending || now).api_url || '';
    el('setAddress').focus();
  });
  el('btnCancelAddress').addEventListener('click', () => { clearError(); closeEdits(); });
  el('formAddress').addEventListener('submit', (event) => {
    event.preventDefault();
    act(() => call('POST', '/api/connection/check', { api_url: el('setAddress').value }), (found) => {
      // The same server as before needs nothing more. A different one needs a key.
      pending = found.api_url === now.api_url ? null : { api_url: found.api_url, name: found.name };
      closeEdits();
      draw();
      if (pending) openKey();
    });
  });

  function openKey() {
    clearError();
    edit(el('rowKey'));
    el('setKey').value = '';
    el('setKey').focus();
  }
  el('btnSwitch').addEventListener('click', openKey);
  el('btnCancelKey').addEventListener('click', () => { clearError(); pending = null; closeEdits(); draw(); });
  el('formKey').addEventListener('submit', (event) => {
    event.preventDefault();
    connect((pending || now).api_url, el('setKey').value);
  });

  // Resetting forgets the key and keeps the address, so only a new key is needed.
  armed(el('btnReset'), 'Yes, reset the key', () => act(() => call('POST', '/api/connection/skip'), (answer) => { pending = null; show(answer); }));
  armed(el('btnBringBack'), 'Yes, bring it back', () => act(
    () => call('POST', '/api/connection/bring-back', { name: now.set_aside[0].name, confirm: true }),
    () => { el('bringText').textContent = 'The app is restarting to put it back.'; }));
  armed(el('btnRestore'), 'Yes, set it aside', () => {
    clearError();
    busy(true);
    call('POST', '/api/sync/restore', { confirm: true }).then(
      () => { el('choiceText').textContent = 'The server’s register has been fetched. The app is restarting to put it in place.'; },
      (e) => { busy(false); fail(e.message); }
    );
  });

  // The desktop app says which version it is. Anywhere else there is nobody to ask, and the panel stays away.
  fetch('/shell/about').then((res) => (res.ok ? res.json() : null)).then((about) => {
    if (!about || !about.version) return;
    el('appVersion').textContent = `Banyan ${about.version}`;
    el('appBuild').textContent = about.note || '';
    el('about').hidden = false;
  }).catch(() => {});

  // Updates: the desktop app checks the release page. Installing is always a
  // click here, so the app never restarts under a cheque.
  function drawUpdate(u) {
    if (!u || !u.status || u.status === 'off') return;
    el('updates').hidden = false;
    const checked = u.checked_at ? `Checked ${new Date(u.checked_at).toLocaleString()}.` : '';
    const [tone, state, sub] = {
      idle: ['', 'Not checked yet', ''],
      checking: ['', 'Checking…', ''],
      current: ['ok', 'Up to date', checked],
      available: ['warn', `Banyan ${u.available} is available`, 'Installing quits Banyan, puts the new version in its place and opens it. The register and calibration are kept.'],
      downloading: ['', `Downloading Banyan ${u.available}… ${Math.round((u.progress || 0) * 100)}%`, 'The download is checked against the release’s signature before anything changes.'],
      ready: ['ok', `Installing Banyan ${u.available}`, 'Banyan is quitting and will open again in a moment.'],
      error: ['bad', 'Couldn’t check for updates', '']
    }[u.status] || ['', u.status, ''];
    el('updDot').className = 'dot' + (tone ? ' ' + tone : '');
    el('updState').textContent = state;
    el('updSub').textContent = u.blocked && u.status === 'available' ? u.blocked : (u.error || sub);
    el('updNotes').hidden = !(u.notes && (u.status === 'available' || u.status === 'downloading'));
    el('updNotes').textContent = u.notes || '';
    el('btnUpdInstall').hidden = u.status !== 'available' || Boolean(u.blocked);
    el('btnUpdCheck').disabled = ['checking', 'downloading', 'ready'].includes(u.status);
    if (u.status === 'downloading' || u.status === 'ready') setTimeout(followUpdate, 500);
  }
  const followUpdate = () => fetch('/shell/update').then((r) => (r.ok ? r.json() : null)).then(drawUpdate).catch(() => {});
  function checkUpdate() {
    drawUpdate({ status: 'checking' });
    fetch('/shell/update/check', { method: 'POST' }).then((r) => (r.ok ? r.json() : null)).then(drawUpdate).catch(() => {});
  }
  fetch('/shell/update').then((r) => (r.ok ? r.json() : null)).then((u) => {
    if (!u || !u.status || u.status === 'off') return;
    drawUpdate(u);
    if (u.status === 'idle' || location.hash === '#updates') checkUpdate();
    if (location.hash === '#updates') el('updates').scrollIntoView({ block: 'center' });
  }).catch(() => {});
  el('btnUpdCheck').addEventListener('click', checkUpdate);
  el('btnUpdInstall').addEventListener('click', () => {
    el('btnUpdInstall').hidden = true;
    fetch('/shell/update/install', { method: 'POST' }).then((r) => (r.ok ? r.json() : null)).then(() => followUpdate()).catch(() => {});
  });

  // Local network: only the desktop app can be asked, so elsewhere the panel stays away.
  const NET = {
    allowed: ['ok', 'Allowed', 'Banyan can check that the printer is switched on.'],
    denied: ['bad', 'Not allowed', 'Banyan can’t check whether the printer is on. Printing still works. macOS won’t ask again, so turn Banyan on in System Settings, then check again.'],
    unknown: ['', 'Not checked yet', 'Lets Banyan check that the printer is switched on. Printing works without it.']
  };
  function drawNet(p) {
    const [tone, state, sub] = NET[p.local_network] || NET.unknown;
    el('netDot').className = 'dot' + (tone ? ' ' + tone : '');
    el('netState').textContent = p.checked_at && p.local_network === 'unknown' ? 'Couldn’t tell' : state;
    el('netSub').textContent = p.checked_at && p.local_network === 'unknown'
      ? 'Nothing on the network answered. Check again with the printer switched on.' : sub;
    el('btnNetOpen').hidden = p.local_network !== 'denied';
    el('rowLocalNet').classList.toggle('is-bad', p.local_network === 'denied');
  }
  function checkNet() {
    const btn = el('btnNetCheck');
    btn.disabled = true;
    el('netDot').className = 'dot';
    el('netState').textContent = 'Checking…';
    fetch('/shell/permissions', { method: 'POST' }).then((res) => (res.ok ? res.json() : null))
      .then((p) => { if (p) drawNet(p); })
      .catch(() => {})
      .finally(() => { btn.disabled = false; });
  }
  // Only a desk prints, so only a desk is asked for the local network. Readers
  // and a first launch never touch it: the check itself is what makes macOS ask.
  let netChecked = false;
  function permissions() {
    if (first() || isReader()) { el('permissions').hidden = true; return; }
    if (netChecked) return;
    netChecked = true;
    fetch('/shell/permissions').then((res) => (res.ok ? res.json() : null)).then((p) => {
      if (!p || !p.local_network || first() || isReader()) return;
      el('permissions').hidden = false;
      drawNet(p);
      // Checked on opening, so the row says something true, and a first-time ask is shown here.
      checkNet();
    }).catch(() => {});
  }
  el('btnNetCheck').addEventListener('click', checkNet);
  el('btnNetOpen').addEventListener('click', () => { fetch('/shell/permissions/open', { method: 'POST' }).catch(() => {}); });

  call('GET', '/api/connection').then(show).catch((e) => { document.body.dataset.mode = 'first'; el('first').hidden = false; fail(e.message); });
})();
