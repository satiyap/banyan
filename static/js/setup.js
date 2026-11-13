/* Settings: the app's link to the Banyan server.
 *
 * First launch is one sheet in two steps: the API URL, which the app checks
 * is a Banyan server, then the user key, which is redeemed there. After that,
 * Settings is a panel in the app's own shell with three rows. The address is
 * settled and shown as text; the key can be switched or reset on its own;
 * and the register row says how the server's copy stands.
 */
(function () {
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
    el('setupLead').textContent = 'Enter your user key. A key works once: connecting uses it up.';
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

  /* Connect with a key, then go to the app unless a choice is still open. */
  function connect(apiUrl, key) {
    act(() => call('PUT', '/api/connection', { api_url: apiUrl, user_key: key }), (answer) => {
      pending = null;
      if (!show(answer)) toApp();
    });
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
  armed(el('btnOverwrite'), 'Yes, replace the server’s copy', () => act(() => call('POST', '/api/sync/overwrite', { confirm: true }), (answer) => { if (!show(answer)) toApp(); }));
  armed(el('btnRestore'), 'Yes, replace this desk’s copy', () => {
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

  call('GET', '/api/connection').then(show).catch((e) => { document.body.dataset.mode = 'first'; el('first').hidden = false; fail(e.message); });
})();
