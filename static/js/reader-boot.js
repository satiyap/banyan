/* Start the members' report pages: the desk's Year and Monthly, and nothing else. */
'use strict';

(function () {
  const READABLE = /^#\/(year|monthly)(\/|\?|$)/;
  // The desk's router falls back to its Desk place, which does not exist here.
  Places.desk = Places.year;

  function show() {
    if (!READABLE.test(location.hash)) { location.replace('#/year'); return; }
    route();
    document.title = `${Places[Router.place].title} · Banyan reports`;
  }

  async function asOf() {
    const note = $('#asOf');
    try {
      const res = await fetch('/api/reader');
      if (res.status === 401) { location.href = '/login'; return; }
      const { latest, app, error } = await res.json();
      // Inside the desktop app there is no browser session to sign out of: the key is reset in Settings.
      if (app) { $('#signOut').hidden = true; addSettingsLink(); }
      if (error) { note.textContent = error; return; }
      note.textContent = latest
        ? `Register as of ${fmtStamp(latest.synced_at)}`
        : 'The treasurer’s desk has not sent a register yet.';
    } catch (e) { note.textContent = ''; }
  }

  hydrateIcons(document);
  $('#btnReload').addEventListener('click', () => location.reload());
  window.addEventListener('hashchange', show);
  show();
  asOf();
  setInterval(() => { if (!document.hidden) asOf(); }, 60000);
})();
