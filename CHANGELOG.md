# Changelog

Key features in each release of the Banyan desktop app. The full history is in the git log.

## 2.2.0 — 2026-11-21

- **Banyan updates itself.** It checks for a new version when it opens and every six hours. When there is one, the title bar says so, and **Settings ▸ This app ▸ Updates** shows what changed with an **Install and restart** button.
- Nothing installs until you click, so an update never interrupts a cheque. The register, calibration and server connection are kept.
- Every update is checked against the release's signature, size and fingerprint before anything changes. If any check fails, the update isn't used and Banyan carries on as it was.
- **Banyan ▸ Check for Updates…** checks straight away.
- This is the last version you need to download by hand.

## 2.1.1 — 2026-11-21

- Keys now say what they are for: `W-` makes this Mac the cheque desk, `R-` shows reports only. A key offered for the wrong role is refused without being used up.
- Before a key is spent, the app says what it will do. Anything that would replace this Mac's register asks first, and asks twice if those records never reached a server.
- Switching a Mac with records to a read key sets its register aside as a file, never deletes it. Settings can bring it back while the register in use has no leaves.
- Taking the server's register records every leaf this Mac had already used, so no leaf is offered for a cheque twice.
- A desk can no longer overwrite a server register it didn't start from. The only choice offered is to set this Mac's register aside and use the server's.
- A reports-only app, and the first-launch screen, no longer ask for local-network access. Only the cheque desk checks the printer.

## 2.1.0 — 2026-11-19

- **Settings ▸ Permissions** shows whether Banyan may use the local network, as a coloured dot with a word, and opens System Settings when it isn't allowed.
- If the permission is refused, the printer shows as "can't check" instead of "offline", and printing is no longer blocked.
- macOS's local-network question now says why Banyan asks.
- The project page tells visitors whether Banyan runs on their computer (Apple silicon Mac, Intel Mac or other).

## 2.0.0 — 2026-11-13

- The desktop app is rewritten in Go on Wails 3. The download drops from about 120 MB to about 7 MB.
- Cheques are rendered with macOS's own WebKit. Pages, cheque layout, calibration and register file are unchanged.
- The Banyan server is rewritten in Go.

## 0.1.0 — 2026-11-13

- First desktop release: write, print and log cheques onto a real bank leaf from one Mac.
- Register of every leaf by cheque book, with statuses and an event trail. Nothing is deleted.
- Payment rhythm: what recurs, how often, and what's due.
- Financial-year and monthly reports, monthly PDF and CSV exports.
- Optional Banyan server: register sync after every change, and read-only reports for members.
