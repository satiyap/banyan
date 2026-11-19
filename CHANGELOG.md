# Changelog

Key features in each release of the Banyan desktop app. The full history is in the git log.

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
