/* Start the desk: calibration first (no geometry, no leaf), then everything
   the places read, then keep it fresh while the page stays open. */
'use strict';

function imageExists(src) {
  return new Promise(resolve => {
    const img = new Image();
    img.onload = () => resolve(true);
    img.onerror = () => resolve(false);
    img.src = src;
  });
}

async function boot() {
  hydrateIcons(document);
  State.tray = stored(TRAY_KEY, []);
  State.printer = stored('banyan.printer', '');
  $('#openCommand').addEventListener('click', openPalette);
  $('#printerChip').addEventListener('click', openHealth);
  $('#printerRefresh').addEventListener('click', recheckPrinter);
  $('#railPrinter').addEventListener('click', openHealth);

  State.photo = await imageExists('/static/leaf-photo.jpg');
  await loadCalibration();
  await Promise.all([loadHealth(), loadDesk()]);
  // Drafts saved by an older desk may carry no paise; price them for the dock.
  for (const d of State.tray) {
    if (d.amount_paise == null && d.amount) {
      const res = await composeFor(d);
      d.amount_paise = res.ok ? res.amount_paise : 0;
    }
  }
  on('desk', rerender);
  on('tray', updateDock);
  window.addEventListener('hashchange', route);
  route();

  // A printer gets switched on and off, and the calibration file gets edited,
  // while the page stays open; without this the desk would show load time.
  setInterval(() => {
    if (document.hidden) return;
    loadHealth();
    loadCalibration();
    loadDesk();
  }, 30000);
  window.addEventListener('focus', () => { loadHealth(); loadCalibration(); loadDesk(); });
}

boot();
