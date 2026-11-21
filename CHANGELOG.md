# Changelog

What each release of the Banyan desktop app does. The full history is in the git log.

## 2.4.0 — 2026-11-21

- Change a category or note from inside the Year and Monthly reports: open an entity, a category or a month's line, then any cheque.
- The edit panel shows what a change moves before saving, and offers Undo.
- The desk's Review lists members' changes, with a count on the rail, and changes several cheques at once.
- "Head" is now called Category everywhere; the old free-text category is now Description.
- Install and restart now reopens Banyan after updating.

## 2.3.0 — 2026-11-21

- Categories: anyone with a key can set the category and note on an entity or a single leaf.
- Members can see every leaf in the reports.
- Changes show who made them; the latest change wins.

## 2.2.1 — 2026-11-21

- Signed and notarized releases.

## 2.2.0 — 2026-11-21

- Updates itself: Settings ▸ Updates ▸ Install and restart.
- Updates are verified before they install.

## 2.1.1 — 2026-11-21

- Keys show their role: W- desk, R- reports only.
- The app says what a key will do before using it.
- A desk can't overwrite a different server register.
- Read-only apps no longer ask for local-network access.

## 2.1.0 — 2026-11-19

- Settings ▸ Permissions shows local-network access.
- Printing works when local-network access is refused.

## 2.0.0 — 2026-11-13

- Rewritten in Go: 7 MB instead of 120 MB.
- The Banyan server rewritten in Go.

## 0.1.0 — 2026-11-13

- Write, print and log cheques on bank leaves.
- Register by cheque book; nothing is deleted.
- Payment rhythm, yearly and monthly reports, PDF and CSV export.
- Optional server sync and read-only member reports.
