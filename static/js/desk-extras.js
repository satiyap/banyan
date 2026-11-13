/* Additions the desktop app makes to the desk's pages (the way into Settings),
   kept in a file of their own. */
'use strict';

/* A way into Settings from the rail, at the bottom, above the printer button where there is one. */
function addSettingsLink() {
  const rail = document.querySelector('.rail');
  if (!rail || rail.querySelector('.rail-settings')) return;
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('class', 'svg-ico');
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', 'M4 7h9M17 7h3M4 17h3M11 17h9M15 4.5v5M9 14.5v5');
  svg.appendChild(path);
  const ico = document.createElement('span');
  ico.className = 'ico';
  ico.appendChild(svg);
  const label = document.createElement('span');
  label.textContent = 'Settings';
  const link = document.createElement('a');
  link.className = 'rail-item rail-settings';
  link.href = '/setup';
  link.append(ico, label);
  rail.insertBefore(link, rail.querySelector('.rail-printer'));
}

// The desk always has Settings. The report pages add it themselves, and only
// inside the app: on the website there is no such screen.
if (!document.body.classList.contains('reader')) addSettingsLink();
