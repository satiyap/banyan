# Banyan Cheque Desk (desktop app) — Agent Guide

## Purpose

This repository is the treasurer's desktop app for Banyan: it writes, prints and logs cheques from one Mac. The primary target is a current State Bank of India multi-city cheque leaf printed on an HP LaserJet Pro MFP M126nw.

The desk began as a small web app, and this repository is now the only home of it. Its behaviour is pinned by the recordings in `tests/golden/`: the answers its rules gave on invented inputs, replayed by the tests. The rules were first written in Python, which is why some code reproduces Python's exact rounding, sorting and text handling. **Do not re-invent a capability or a screen that already exists; change it where it is.**

The app is called **Banyan**. `static/index.html`, `static/js/core.js`, `static/desk.css` and `static/favicon.svg` are pinned by sha256 in `tests/golden/http.json`: after changing one on purpose, put its new hash there in the same commit.

Treat cheque output as precision stationery. A visually attractive preview is secondary to physical dimensions, safe field placement, predictable printer transforms, and an auditable workflow.

## Repository map

Three processes: the Electron main process (window, menus, and the Chromium that turns a composed page into a PDF), the local server (all logic and data, with no Electron imports), and the page (plain HTML, CSS and JavaScript, loaded from the server over loopback).

- `src/main/index.ts` — window and startup order. The server starts first; the window is given a per-launch secret as a cookie and loads the desk from it.
- `src/main/server.ts` — starts the server process, decides where its files live, and answers its requests to render a page to PDF. Development builds switch printing off here.
- `src/main/pdf.ts` — one HTML file to one PDF with the app's own Chromium. The page's own `@page` size is honoured as is; nothing is scaled or given margins.
- `src/main/menu.ts` — the menu bar. Places are the desk's own hash routes.
- `src/server/app.ts` — HTTP API and route wiring. Also the place the two safety rules live: nothing prints without a verified page box, and nothing is recorded as printed without the treasurer's confirmation.
- `src/server/models.ts` — request and response shapes (zod), including the 422 error format the frontend reads.
- `src/server/calibration.ts` — loads and validates `cheque-calibration.json`. Refuses to run against a leaf size it was not measured for.
- `src/server/render.ts` — cheque → self-contained HTML → PDF, with the page box verified on every render. This composition was checked against a ruler on real hardware; treat its geometry as measured, not chosen.
- `src/server/host.ts` — who renders the PDF: the desktop app's Chromium when the server runs inside the app, an installed Chrome with the verified flags when it runs on its own.
- `src/server/printing.ts` — print queue submission through `lp` (`scaling=100`, `fit-to-page=false`) and job tracking. Holds the switch that keeps development builds and tests away from the printer.
- `src/server/amount.ts` — rupee parsing and Indian-system words. Money is integer paise throughout; floats never hold a value.
- `src/server/frequency.ts` — per-entity cadence detection, next-due prediction, and register summaries. Median/MAD based so one emergency cheque cannot redefine a monthly rhythm. One-off entities never get a cadence or a due date.
- `src/server/reports.ts` — financial-year reports (April–March, Q1 = Apr–Jun) by entity, expense head, printed payee and month. Sums in paise on the server; the page only lays them out.
- `src/server/reportPdf.ts` — the monthly report as an A4 PDF. Paperwork for a committee; it never goes near the printer.
- `src/server/db.ts` — SQLite register through `node:sqlite`. Append-mostly: cheques are voided, never deleted, and every state change writes an event row. The file format is fixed: a register written by any earlier version of the desk opens unchanged.
- `src/server/leaves.ts` — cheque books and the register: book ranges, next leaf, the 80%-used alert, gaps and duplicate numbers. A book is a number range; nothing is stored on cheque rows.
- `src/server/dating.ts` — entity date rules (post-dating) and post-dated reminders, answered as events.
- `src/server/pycompat.ts` — small helpers that reproduce the Python behaviour the recordings expect (string stripping, `repr`, integer parsing, code-point ordering, ISO dates).
- `static/cheque-layout.js` — **field placement, used by both the print renderer and the on-screen preview.** There is deliberately no second implementation; when preview and print are drawn separately they drift, and the drift is found on a real leaf.
- `static/cheque.css` — the geometry CSS, likewise shared by preview and print.
- `static/{index.html,desk.css}` and `static/js/*.js` — the frontend (Desk, Tray, Register, Year, Monthly, ⌘K). Plain scripts in load order, no build step, no npm. Every cheque is written as a Tray row; the leaf above the grid is a read-only preview of the selected row. Leaves are drawn only through `ChequeLayout.render` (`leafview.js`), never a second copy of the geometry.
- `templates/cheque_print.html.tpl` — the print page. Data is inlined, never fetched: it is opened over `file://`, where a blocked fetch would silently print defaults.
- `cheque-calibration.json` — the measured field geometry the app ships with. On first launch it is copied into the app's data folder, and that copy is the single source of truth from then on: it holds measurements of the treasurer's own printer. Bump `version` on any change.
- `scripts/verify-print.test.ts` — renders through the real composition code and measures where the ink lands, then checks it against the calibration. Run `pnpm verify-print` after any change to the renderer, the shared layout module, the print CSS or the template.
- `scripts/render-check.cjs` — renders one invented cheque with the app's Chromium and with an installed Chrome and reports both page sizes. Run `pnpm render-check` after upgrading Electron.
- `tests/golden/` — the recordings: what the desk's rules answered on invented inputs, as JSON, plus two invented registers. The tests replay them. Nothing regenerates them; a recorded case is edited by hand, with the change that requires it.
- `e2e/app.spec.ts` — opens the real app against a throwaway data folder. It never prints.
- `build/icon.svg` — the app icon: the existing Banyan mark on the macOS icon shape.

**Where the data lives.** The register is `~/Library/Application Support/Banyan/banyan.db`, beside the calibration copy and rendered PDFs. It is financial data: never seed, reset, or bulk-edit it as a shortcut, and never commit a register to this repository (`data/` and `*.db` sidecars are git-ignored). There is deliberately no demo data. The preview backdrop `static/leaf-photo.jpg` ships with the app and is committed. It is a photograph of a real leaf with the account number, account name and MICR band blacked out, the anti-copy (pantograph) patch blurred and the resolution halved to about 127 dpi, and a test pins its hash: never replace it with an unredacted or sharper photograph, and never commit the original. A copy in the data folder's `static/` is served in preference to the shipped one.

**Printing during development.** A leaf is usually loaded in the printer. `pnpm dev`, `pnpm start` and the tests cannot print: the server refuses to hand anything to the print queue unless the app is packaged, or `BANYAN_ALLOW_PRINT=1` is set deliberately. Never set it to make a test pass.

The frontend has **no build step and no package manager**: it is plain HTML, CSS and JavaScript served by the local server. Do not add a frontend framework, bundler, or external CDN asset unless the user asks for one. The server and the Electron shell are TypeScript, built by electron-vite into two files with their dependencies inlined.

## Authoritative physical facts

Use these values unless the user supplies a new physical measurement:

- Detached cheque leaf: **204 mm wide × 92 mm high**.
- Browser print page: **92 mm wide × 204 mm high**, portrait.
- The landscape cheque coordinate space is rotated 90 degrees onto that portrait printer page.
- Target printer: **HP LaserJet Pro MFP M126nw**.
- Paper is fed sideways because the cheque's normal 92 mm page length is below the printer's supported feed length.
- The photographed leaf is a current SBI multi-city design and is visibly marked **CTS-2010**. CTS-2010 is a security/layout standard, not an artwork year.
- The MICR-safe band begins at approximately `y = 76 mm` and continues to the bottom edge. Dynamic content must never enter this band.

Never silently revert to the former `202 × 92 mm` cheque or `100 × 202 mm` virtual-page assumptions. If dimensions change, update every geometry representation together.

## Geometry contract

All cheque field coordinates are expressed in millimetres from the physical leaf's top-left corner before rotation:

- `x = 0` is the left edge of the landscape cheque.
- `y = 0` is the top edge.
- `CHQ_W` and CSS cheque width must remain `204`/`204mm`.
- `CHQ_H` and CSS cheque height must remain `92`/`92mm`.
- `PAGE_W` and print-root width must remain `92`/`92mm`.
- `PAGE_H` and print-root height must remain `204`/`204mm`.
- `@page` must remain `size: 92mm 204mm; margin: 0` unless a tested printer constraint requires a documented change.

The normal transform must map all four cheque corners exactly onto the physical page:

```text
(0, 0)     -> (92, 0)
(204, 0)   -> (92, 204)
(0, 92)    -> (0, 0)
(204, 92)  -> (0, 204)
```

The flipped transform must cover the same `92 × 204 mm` page in the opposite feed orientation.

When changing dimensions, search for all hard-coded occurrences in:

- `.cheque-space`
- `@page`
- the print template's `#root` and `#sheet`
- `CHEQUE_W_MM`, `CHEQUE_H_MM`, `PAGE_W_MM`, `PAGE_H_MM` in `src/server/calibration.ts`, and `CHQ_W`/`CHQ_H`/`PAGE_W`/`PAGE_H` in `cheque-layout.js`
- preview scaling
- SVG `width`, `height`, `viewBox`, border and MICR lines
- setup instructions and paper-size labels
- test-sheet labels and feed documentation

Do not introduce CSS scale transforms into print output. Calibration should use millimetre translation and per-field coordinates, not page scaling.

## Current field-layout intent

`cheque-calibration.json` is the source of truth and the only copy. The intended areas are:

- Date: eight individually centred `DDMMYYYY` digits in the top-right boxes.
- Payee: begins after the printed `PAY` label and ends before `OR ORDER`.
- Words line 1: begins after `RUPEES` and may use most of the first ruled line.
- Words line 2: begins near the left edge and stops before the numeric amount box.
- Numeric amount: begins to the right of the cheque's preprinted rupee-symbol cell, and always terminates with `/-` so digits cannot be appended.
- `OR ORDER` strike: optional and **off by default**.
- Star filling: optional and **off by default**.
- `A/C PAYEE ONLY`: **on by default**, rendered as a compact 45-degree top-left corner crossing between two parallel lines.

The crossing is a bounding box, not a padding trick. Its `cross` entry takes `x`/`y` (top-left corner), `w`/`h` (the box; `h` **is** the gap between the two rules, which are the box's top and bottom borders), `angle`, `origin` (`top left` or `bottom left`), `textDx` (caption nudge, negative = left), `size`, and `tracking`. The caption is centred by flexbox, so `textDx` moves it one-for-one — do not reintroduce asymmetric padding to shift text.

The crossing is deliberately anchored inside the printer's non-printable margin and its ends are accepted as clipped; roughly half the bar prints, and the two rules plus the caption land in the printable middle. This is a settled decision, not a bug. If its width or angle changes, recalculate the rotated endpoint (`end = (x + w·cos θ, y + w·sin θ)`) rather than judging by angle alone.

Field positions derived from the reference photograph are starting values, not proof of printer alignment. Printer feed tolerances must be handled by a plain-paper overlay test and global `dx`/`dy` calibration.

### Hardware printable area

The M126nw PPD (`/private/etc/cups/ppd/HP_LaserJet_Pro_MFP_M126nw.ppd`) declares `ImageableArea` inset by `11.9906pt` = **4.23mm on all four edges**. Nothing closer than that to a paper edge receives toner, whatever the CSS says. Keep fields at or beyond 5mm from the edges, the crossing excepted. When a mark appears missing rather than misplaced, check this before adjusting coordinates.

### Reading a printed sheet

The leaf is landscape (204 × 92) but the page is portrait (92 × 204), so a printed sheet must be rotated 90° before comparing against a cheque. Page and cheque coordinates relate as:

```text
page_x = 92 - cheque_y        cheque_y = 92 - page_x
page_y = cheque_x             cheque_x = page_y
```

A field measured 75mm from the page's left edge is therefore at cheque `y = 17`, not misplaced. Confirm which orientation a measurement was taken in before treating it as an offset.

## Reference-image handling

The HEIC reference may be converted to a temporary PNG for local inspection. Write conversions only under `/tmp` or another temporary directory. Preserve the original file unchanged.

When extracting layout information:

1. Correct orientation.
2. Identify all four physical cheque corners.
3. Account for perspective before converting pixels to millimetres.
4. Use the measured `204 × 92 mm` leaf as the scale.
5. Measure writable baselines and box interiors, not merely nearby labels.
6. Keep real account numbers, cheque numbers, branch/customer identifiers, and MICR data out of code and prose.

The on-screen preprint mock must remain generic. It may resemble the SBI layout but must not reproduce the user's banking identifiers.

## Calibration and persisted state

`cheque-calibration.json` is the only place measurements live. There is no second
copy and no per-browser override: the server reads the file, and re-reads it when
its mtime changes, so an edit takes effect on the next request without a restart.
A broken edit keeps the last good copy and is reported through `/api/health`
rather than crashing the app or, worse, printing defaults.

Increment `version` whenever a field position, page geometry or crossing geometry
changes. Nothing depends on the number functionally any more — it is the record
of which measurements a given cheque was printed under, and it is stored on every
register row as `cal_version`.

Text is placed by the browser's layout, which snaps to whole CSS pixels, so
positions land on a **0.2646mm** grid. A 1.2mm nudge becomes 1.058mm. That is far
inside the printer's own feed tolerance, but it means sub-0.26mm calibration
steps are not achievable through this path — do not chase them.

Global calibration is translation only:

- `dx`: move all dynamic fields left/right in cheque space (prints as page **y**).
- `dy`: move all dynamic fields up/down in cheque space (prints as page **−x**; minus moves toward the leaf's top edge, the date-box side).
- `flip`: change feed orientation by 180 degrees.

Per-field adjustments remain available for actual leaf variations. Keep units in millimetres and reasonable bounds on global nudges.

### Paper mode

`paper` selects the declared page: `exact` (custom 92 × 204mm, recommended) or `a4`, which prints the same 92 × 204 block at 1:1 on an A4 sheet offset by `a4x`/`a4y`. `@page` size cannot be set from a style property, so it is swapped through a dedicated stylesheet at runtime. A4 keeps geometry identical and only changes where the block sits, but registration then depends on feeding the leaf to the same spot each time; prefer `exact` for real cheques.

## Test-sheet requirements

The test sheet is diagnostic output, not decorative output. It should make scale, clipping, rotation, and offsets measurable.

Important distinction:

- The current physical page is `92 × 204 mm`.
- An outline placed only `0.2 mm` from the edge may be clipped by the printer's non-printable area and is therefore a poor scale reference.
- Prefer clearly labelled inset reference geometry, such as a rectangle at least 5–10 mm from every edge, dimension ticks, and a known 100 mm line.

If a user reports that a printed box is wrong, request either:

- a straight overhead photo/scan of the printed test beside a ruler, or
- measured width, height, left offset, and top offset.

Calculate independently:

- horizontal scale error
- vertical scale error
- horizontal translation
- vertical translation
- rotation/skew

Do not "fix" scale errors by guessing field coordinates. Run `pnpm verify-print`
first: it will tell you whether the renderer still agrees with the calibration,
which separates a composition bug from the printer's own feed offset.

## The print path

There is no print dialog. The server composes the page, the app's own Chromium
turns it into a PDF, and the server hands that PDF to the print queue itself, with `scaling=100` and `fit-to-page=false` pinned
on the `lp` call. This is deliberate: a dialog can silently rescale, refit or
substitute the paper size, and the result is indistinguishable from a geometry
bug. Removing it means a bad print can only come from the composition or the
printer's feed.

Every render verifies its own page box and refuses to return anything that is not
92 × 204mm at 1:1 on a single page.

The printer has non-printable margins even when CSS requests zero page margin.
Zero `@page` margin defines the coordinate system; it does not guarantee toner at
every physical edge.

## Cheque-content rules

- Require a valid date, non-empty payee, and positive amount before real printing.
- Date output is exactly eight digits in `DDMMYYYY` order.
- Parse at most two paise digits and cap values below ₹100 crore unless requirements change.
- Convert amounts using the Indian crore/lakh/thousand system.
- Keep print text dark, high contrast, and free of colour dependencies.
- Never print inside the MICR band or signature area.
- Long payees and amount words should shrink only within a legibility floor and must surface an overflow warning.
- Use `textContent` or correct HTML escaping for user-provided data.
- CSV export must neutralise spreadsheet-formula prefixes in every user-controlled column.

The app prints onto a genuine bank-issued cheque leaf. It must never generate security artwork, MICR characters, account identifiers, signatures, or a substitute cheque on blank paper.

## Register behaviour

The register (`banyan.db` in the app's data folder) is the treasurer's record of which physical
leaves have been committed. It is not a bank ledger, but it is the only record
that a numbered leaf was consumed, so treat it as financial data.

- Statuses are `queued` → `printed` | `failed`, plus `logged` (recorded without printing) and `void`.
- **A queued job is not a printed cheque.** CUPS accepting a job says nothing about whether toner landed correctly on a hand-fed leaf. Only the treasurer's confirmation moves a cheque to `printed`.
- Status changes are guarded: `settle` and `void` change a cheque only if it is still in the status they found it in, in the same statement. Do not reintroduce read-then-write transitions.
- **Batches print their first cheque alone.** The rest cannot be sent until that first leaf is confirmed as printed; a misprinted first leaf is spoiled and the cheque reprints on the next one. Each batch step claims its stage (`move_batch`) before touching the printer, leaves are numbered only as CUPS accepts them, and sending stops at the first refusal so no cheque lands on the wrong leaf. Keep all three properties.
- A leaf number already in the register, in any status, is never reused: a misprinted or voided leaf was still physically used.
- Cheques belong to an entity by `entity_id`. Several entities may print the same payee name; the entity name tells them apart, and an ambiguous payee is refused rather than guessed.
- An entity's `kind` is `regular`, `one_off` or `per_cheque`. One-offs stay out of the rhythm and are grouped together in reports. A `per_cheque` entity (GST, paid to a fresh CRN each time) keeps a rhythm but prints a payee typed on each cheque, starting from its optional `payee_prefix`; its `payee_name` is only a label, is never matched against a leaf, and renaming it never rewrites its cheques.
- A cheque may be counted against another entity only if that entity would have printed it (same payee name, or `per_cheque`). The leaf's payee, amount, date and status never change; each move writes an `entity-moved` event.
- A `failed` cheque must not be flippable back to `printed`. Correcting that is a void, which leaves a trail.
- `queued`, `printed` and `logged` count as leaves issued; `failed` and `void` do not, and the frequency analysis excludes them.
- Never delete rows, reset the database, or bulk-edit it as a shortcut. Void instead.
- Every state change writes an `events` row. Do not add a state transition that skips it.
- Blank leaves (used up without a cheque) live in `blank_leaves` and count as used by `cheque_no_used`. Events not about one cheque (batches, books, blank leaves, plain-paper tests) carry `events.subject` (`batch:14`, `book:2`, `leaf:004412`, `test`).
- Schema additions are new tables or defaulted columns only (`ADDED_COLUMNS` in `src/server/db.ts`), taken with a one-time copy beside the register (`banyan.db.pre-desk-v2`, or `banyan.db.pre-<column>` for a column added after desk v2), so the previously deployed desk still reads and writes a migrated register. Keep it that way: no new cheque statuses, no column rewrites.
- Exports must not silently omit cheque number, date, payee, amount, or status, and must neutralise spreadsheet formula prefixes (`=`, `+`, `-`, `@`) in every user-controlled column.
- Money is integer paise end to end. A float must never hold an amount.

## The Banyan server

The server and the desk's sync with it are built and tested; nothing is deployed until the owner says so.

- `src/backend/store.ts`: keys, tokens, numbered register copies. `src/backend/app.ts`: the HTTP API, the admin routes and the members' pages. `src/backend/index.ts`: the entry point, configured by environment.
- `src/server/sync.ts` and `src/server/connection.ts`: the desk's side. Routes live in `src/server/app.ts` under `/api/connection` and `/api/sync`.
- Pages: `static/setup.html` (the desk's connection screen), `static/login.html` and `static/reader.html` (the members' sign-in and reports). The members' pages load the desk's own `year.js` and `monthly.js` unchanged.
- The server reads reports through the desk's own app (`createApp`) pointed at the latest register, behind a short allow-list (`READER_API`). Widening that list exposes more of the register to members: do not do it without being asked.
- The server must never gain a route that writes a cheque or prints, and never hand the register file to anything but a write token.
- Whenever the desk and the server disagree about which register is newer, the code must stop and ask. Never resolve it automatically in either direction.
- Tests: `src/backend/backend.test.ts`, and `e2e/app.spec.ts` runs the app against a real local server. All registers in them are invented.

Decisions already made by the owner. Do not reopen them without being asked:

- **The desk works without a server.** Skipping the connection is a full answer. It switches off only the features that need the server, of which register backup is the first.
- **One server, always, and its address stays out of the app and out of this repository.** The app is meant to be downloadable by anyone, so it ships with no API URL: the treasurer is given the address together with the key. Do not add a default address, and do not write the server's hostname into code, tests or docs here.
- **Keys.** One key per login. There are two privileges: one that writes, and read for everyone else. A key becomes single-use: the desk redeems it once for a long-lived token, and the server invalidates the key. Key and token are kept in plain files in the data folder on purpose. The server is what protects them, not the client.
- **Who writes.** A desk holding the write token writes cheques. The register must never hold two live cheques with the same leaf number.
- **One write token at a time.** Issuing a new write key invalidates the previous one. A handover is: issue a key, the new desk redeems it and restores, the old desk becomes read-only. This holds until there are full roles.
- **Sync, not backup.** There is no separate backup. After every write to the register (a cheque written, updated, confirmed, voided) the whole register file is sent to the server, and the server's copy is the live remote copy. A desk can restore from it. That is the point of the server.
- **When it syncs.** After every write. Before quitting and at launch are the catch-up for anything that did not get through. The desk shows when it last synced, so a stale remote copy is visible.
- **What readers see.** Reports only, to begin with: on the website, or in the desktop app connected with a read key, which then shows the same report pages and nothing else. A reader's app must never receive the register file.
- **Hosting.** The server runs as one container with a data volume, behind an HTTPS ingress. Where it runs, its address and its admin token are the owner's deployment details and are kept out of this repository. An admin page for issuing keys is wanted and not yet scoped.
- **Cutover.** Done on 2026-11-12: the earlier web desk's register was imported into the desktop app. The web desk is to be switched off once the owner is satisfied.

## Editing conventions

- Make the smallest coherent change. Existing behaviour is kept, not redesigned; where the code and a recording in `tests/golden/` disagree, the recording is the reference until the owner says otherwise.
- Keep field placement in `static/cheque-layout.js` only. If you find yourself writing a second copy of the geometry for the preview, stop — that is the bug this structure exists to prevent.
- Keep the frontend build-free: no bundler, no framework, no CDN asset.
- Preserve accessibility labels, keyboard behaviour, and visible focus.
- Render user-supplied text with `textContent`, never by string-building HTML. A payee name is data.
- Do not add real banking data to examples, fixtures, or demo seeds.
- Keep the leaf guide (preview-only artwork) separate from the printed fields.
- Comments should explain physical-print reasoning, not narrate obvious code.

## Required verification

### 1. Geometry

```sh
pnpm verify-print
```

Renders through the real composition code and measures the result, so it catches a renderer
that has drifted from the calibration — a field moved in `cheque-layout.js`, a
scale error introduced in `cheque.css`, a field pushed into the MICR band. Run it
after **any** change to `render.ts`, `cheque-layout.js`, `cheque.css` or
`cheque_print.html.tpl`. Fails loudly; needs poppler for `pdftoppm` and an
installed Google Chrome. After upgrading Electron, also run `pnpm render-check`:
the app prints with its own Chromium, and that check confirms it still produces
the same page as the reference renderer.

Note what it cannot catch: it validates the render against the calibration, so
both moving together — an edited measurement — passes by construction. That is
what the plain-paper test is for.

### 2. Everything compiles and still matches the recordings

```sh
pnpm typecheck && pnpm test && pnpm e2e
```

`pnpm test` replays the recordings in `tests/golden/`. A failure there means
behaviour changed; do not edit a recording to make it pass unless the change in
behaviour is the one the owner asked for.

### 3. Geometry invariants

Confirm CSS, JavaScript and the transforms still agree on `204 × 92` (leaf) and
`92 × 204` (page).

### 4. Default-state check

On the Write form: crossing on, strike-out off, star-fill off.

### 5. Sensitive-data check

No account, cheque, MICR or customer identifier from the reference photograph may
appear in code, fixtures, comments, documentation or the UI. Generic mock values
are acceptable only when clearly fictitious.

### 6. Physical test handoff

For changes affecting page size, rotation, coordinates, font metrics, the
crossing or the print CSS, say that a plain-paper overlay test remains required.
No code-level check can certify the printer's physical feed offset.

## Completion standard

A cheque-layout task is complete only when:

- source geometry is internally consistent;
- JavaScript parses;
- persisted calibration behaviour is intentional;
- preview-only content cannot leak into real print output;
- no dynamic field enters the MICR or signature zones;
- the user is told what must be verified on plain paper;
- any unverified physical assumption is explicitly labelled as such.

