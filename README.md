<p align="center">
  <img src="build/icon.png" width="112" alt="">
</p>

<h1 align="center">Banyan — Cheque Desk</h1>

<p align="center">
  The treasurer's desktop app for Banyan: writes, prints and logs cheques from one Mac,<br>
  so the only thing left to do by hand is sign them.
</p>

The desk began as a small web app and became this desktop app without changing how it behaves: the screens, the cheque layout, the print CSS, the print template and the calibration carried over, and every rule is held in place by recorded answers that the tests replay.

## Why a desktop app

The cheque desk has to sit on the same network as the printer. A read-only view of the register for other members has to be reachable from outside that network. One hosted app cannot be both.

So the app is split in two, and this repository holds both halves:

- **The desk (this app).** Runs on the treasurer's Mac. Holds the register, controls the printer, works without internet.
- **The Banyan server (`src/backend`).** Holds a copy of the register and shows members the reports, read-only, in a browser. The desk pushes every change out to it; nothing ever reaches in to the desk.

## What it does

- **Write** — date, payee, amount in words and figures, and the A/C payee crossing, printed onto a genuine bank leaf at measured millimetre positions.
- **Rhythm** — works out which payments recur, how often, roughly how much, and what is due or overdue.
- **Register** — an auditable record of every leaf the desk has committed, by cheque book.
- **Reports** — the financial year by expense head, quarter and entity; month-by-head totals as a PDF; CSV exports.

## Run it

You need macOS on Apple silicon, Node 22 or newer, and pnpm 11.

```sh
pnpm install
pnpm dev
```

`pnpm dev` **cannot print.** A cheque leaf is usually loaded in the printer, so development builds and tests refuse to hand anything to the print queue. A packaged build prints normally:

```sh
pnpm package:dir
open dist/mac-arm64/Banyan.app
```

## Commands

| Command | What it does |
| --- | --- |
| `pnpm dev` | Runs the app. Printing is switched off. |
| `pnpm build` | Builds the main process and the server into `out/`. |
| `pnpm start` | Runs the built app without packaging it. Printing is switched off. |
| `pnpm typecheck` | Type-checks everything. |
| `pnpm test` | Replays the recorded answers in `tests/golden/` against the code, and tests the server and sync. |
| `pnpm e2e` | Builds, then opens the real app on a throwaway data folder against a local Banyan server: connects, renders a test sheet, visits every screen, syncs, and signs in to the members' pages. Never prints. |
| `pnpm verify-print` | Renders cheques and measures where the ink lands against the calibration. Needs poppler and Google Chrome. |
| `pnpm render-check` | Renders one invented cheque with the app's Chromium and with installed Chrome and reports both page sizes. |
| `pnpm package` | Builds a `.dmg` into `dist/`. |
| `pnpm package:dir` | Builds just the `.app` into `dist/mac-arm64/`. |
| `pnpm icons` | Regenerates the app icon from `build/icon.svg`. |
| `pnpm server` | Runs only the desk's local server under plain Node (after `pnpm build`). |
| `pnpm build:backend` | Builds the Banyan server into `out/backend/`. |
| `pnpm backend` | Runs the Banyan server (after `pnpm build:backend`). |
| `pnpm release 0.2.0` | Tags and pushes a release of the desktop app. |

## How it fits together

```
┌──────────────────────── the app ─────────────────────────┐
│                                                           │
│  the desk's pages ── same /api routes ──▶ local server    │
│  (static/, unchanged)                        │            │
│                                              ▼            │
│  main process ◀── "render this page" ── compose cheque    │
│  built-in Chromium ── PDF ─────────────▶ check page size  │
│                                              │            │
│                                              ▼            │
│                                   print queue (lp), register
└───────────────────────────────────────────────────────────┘
```

- **The pages** are plain HTML, CSS and JavaScript with no build step. The window loads them from the local server.
- **The local server** (`src/server`) holds all the logic and data: the routes, the JSON, the register file. It listens on the loopback address only and requires a secret that is regenerated on every launch and given only to the app's own window.
- **The main process** (`src/main`) owns the window, the menus and the Chromium that turns a composed page into a PDF.

## Printing

Nothing about how a cheque is printed was redesigned.

1. The server composes one self-contained page from the print template, `cheque.css`, `cheque-layout.js` and the cheque's data.
2. The app's own Chromium prints that page to a PDF. No print dialog is involved, so nothing can rescale it.
3. The PDF's page size is read back and must be 92 × 204 mm within 0.5 mm, on exactly one page. Otherwise it is refused.
4. The PDF goes to the print queue through `lp` with a custom 92 × 204 mm media size, fit-to-page off and scaling pinned at 100%.
5. A finished job is not a printed cheque. It waits as "awaiting confirmation" until the treasurer says the leaf came out right. A batch prints its first leaf alone and waits for that verdict.

Step 2 uses the Chromium that ships inside the app, not whichever browser is installed, so every install renders identically and the renderer only changes when the app is updated. `pnpm render-check` compares it with an installed Google Chrome.

**Before the first real cheque from this app, print the test sheet on plain paper cut to leaf size and hold it against a real leaf.** No software check can certify where a printer feeds paper.

## Connecting to the Banyan server

On first launch the app asks two things, one after the other: the server's API URL, then a user key. No address ships with the app: anyone may download it, but only someone given both can reach the association's server. **Skip for now** is a full answer: the desk writes and prints cheques without a server, and is not asked again. **Settings** (in the rail, or **Banyan ▸ Settings…**, ⌘,) reopens the screen, and There, **Switch key** replaces the key for the same server, **Reset key** forgets the key but keeps the address, and **Change the address** is for the rare case the server moves.

- **Step 1, the address.** The app asks it `GET /api/v1/info`. Only a Banyan server this version of the app understands is accepted, and the key is not asked for until one answers, so a mistyped address never receives it.
- **A key works once.** Connecting redeems it for a token the app keeps (`connection.json`), and the key is spent.
- **The key decides what the app is.** A write key makes it the cheque desk. A read key makes it a reports-only app: the Year and Monthly pages, fetched from the server each time, with the desk, the register and printing closed. No register is downloaded to a reader's Mac, and one already on it is neither shown nor sent.
- **Every change is copied to the server**, straight away, and once more when the app quits and when it opens, in case an earlier attempt did not get through. The title bar shows when the server last had the register, or why it does not.
- **One desk writes at a time.** Connecting a new desk retires the one before it, which then says so in its title bar.
- **When the desk and the server hold different registers**, nothing is sent or fetched. The connection screen asks which one is right. Choosing the server's fetches it and restarts the app; the register it replaces is kept beside it as `banyan.db.before-restore-<time>`, never deleted.

That last case is how a register moves to a new Mac: install, connect with a new write key, choose the server's register.

## The Banyan server

`src/backend` is a small Node server with three jobs:

0. **Discovery.** `GET /api/v1/info` is open to anyone and says only that this is a Banyan server, its name, its API version and what it can do. Nothing about the register.
1. **Keys.** `POST /admin/keys` (with the admin token) issues a single-use key, `write` or `read`, valid for a week. Whoever redeems it gets a long-lived token.
2. **The register.** The writing desk sends the whole register file after every change (`PUT /api/v1/register`). Each accepted copy is numbered, and a desk must say which number it started from, so a desk that has fallen behind cannot overwrite a newer register. The last 200 copies are kept.
3. **Reports for members.** At `/`, anyone with a key signs in and sees the desk's own Year and Monthly pages, drawn from the latest register by the same code. Nothing there can write, and the register file itself is only ever handed to the writing desk.

Run it locally:

```sh
pnpm build:backend
BANYAN_ADMIN_TOKEN=$(openssl rand -hex 32) BANYAN_INSECURE_COOKIES=1 pnpm backend     # http://127.0.0.1:8322
```

Issue a key (there is no admin page yet):

```sh
curl -X POST https://<the server>/admin/keys \
  -H "Authorization: Bearer $BANYAN_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"role": "write", "label": "treasurer"}'
```

| Setting | What it is |
| --- | --- |
| `PORT` | Where it listens. 8322. |
| `DATA_DIR` | Keys, tokens and register copies. `/data` in the container. |
| `BANYAN_ADMIN_TOKEN` | Secret. Lets its holder issue keys. At least 32 random characters (`openssl rand -hex 32`), or the server refuses to start. Unset, no keys can be issued. Wrong tries are limited to eight per address every ten minutes. |
| `BANYAN_SERVER_NAME` | What the server calls itself to an app that connects, such as the association's name. |
| `BANYAN_INSECURE_COOKIES` | `1` only when running over plain http on one machine. |

It ships as a container (`Dockerfile`, runs as user 1000, health check at `/healthz`) and needs one volume for its data. Where it is deployed is not recorded here.

## Releases and automation

| Workflow | Runs when | What it does |
| --- | --- | --- |
| `ci.yml` | A push to `main` or a pull request that changes code. Docs and `*.md` do not start it. | Typecheck, unit tests, the app end to end. |
| `image.yml` | A push to `main` that changes something the server image contains. | Builds `ghcr.io/satiyap/banyan` on a GitHub-hosted ARM64 runner. |
| `release.yml` | A `v*` tag. | Builds `Banyan-mac-arm64.dmg`, tests the packaged app, publishes a GitHub release. |

To release the desktop app: `pnpm release 0.2.0`. It sets the version, tags `v0.2.0` and pushes; the tag builds and publishes the release. The latest disk image is always at `releases/latest/download/Banyan-mac-arm64.dmg`.

A release is signed and notarized when the repository holds an Apple Developer ID certificate (the secrets are listed at the top of `release.yml`). Without one it is ad-hoc signed: on another Mac, macOS blocks it the first time and it has to be allowed under System Settings ▸ Privacy & Security ▸ Open Anyway.

## Where the data lives

Everything is in `~/Library/Application Support/Banyan/`:

| File | What it is |
| --- | --- |
| `banyan.db` | The register. Financial data. |
| `cheque-calibration.json` | The measured field positions for this desk's printer. Copied from the shipped default on first launch and yours to edit from then on; the app picks up edits without a restart. |
| `connection.json` | The server's address and this desk's token, or the fact that connecting was skipped. Plain text on purpose: the server can retire a token, and does when another desk takes over. |
| `sync.json` | Which copy of the register the desk and the server last agreed on. |
| `cheques/` | Rendered PDFs. Disposable. |
| `static/leaf-photo.jpg` | Backdrop for the on-screen preview: a photograph of a real leaf with the account number, account name and MICR band blacked out, the anti-copy patch blurred and the resolution halved. Ships with the app; a copy in the data folder's `static/` replaces it. |

**Help ▸ Show Register in Finder** and **Show Calibration File in Finder** open them.

The register starts empty, and no register is ever committed to this repository.

### Bringing over an existing register

The register's file format has not changed since the first version of the desk, so an older `banyan.db` opens as it is. To bring one in, quit the app and copy the file to `~/Library/Application Support/Banyan/banyan.db`. From then on write cheques from one copy only: two desks writing separate copies will hand out the same leaf numbers.

## Tests

The desk's behaviour is pinned by recordings in `tests/golden/`: the answers its rules gave on invented inputs, which the tests replay and require to come out identical. The rules were first written in Python and the recordings were taken from that code, which is why a few helpers (`src/server/pycompat.ts`) reproduce Python's rounding, sorting and text handling exactly.

| Area | Recorded cases |
| --- | --- |
| Amounts in words and figures | 10,684 |
| Calibration loading and validation | 2,816 |
| Leaves and cheque books | 2,674 |
| Date rules and reminders | 44,120 |
| Cadence and summaries | 225 register runs |
| Financial-year and monthly reports | 1,200 calls |
| The register (SQLite) | 513 operations in 17 scenarios, plus opening a register written by an earlier desk |
| The HTTP API | 806 requests across every route but one |

The Banyan server and desk-to-server sync have no recordings; `src/backend/backend.test.ts` tests them directly, including a handover between two desks.

A failing replay means behaviour changed. If the change is intended, update the recorded case it affects in the same commit; there is no tool that rewrites a recording wholesale, on purpose. All recorded data is invented: fictitious payees, amounts and leaf numbers.

## What is not done yet

- **An admin page.** Keys are issued with `curl`.
- **Member sign-in is by key.** One key per browser, with no way yet to see or revoke sessions except through the admin routes.
- **The monthly PDF on the members' pages.** It is drawn by the desk's browser engine, which the server does not carry.
- **A plain-paper test from this app.** The rendered page is identical to the reference renderer's, but nothing has been printed from the desktop app.
- **Signing and updates.** Without an Apple Developer ID in the repository's secrets, releases are ad-hoc signed. Auto-update is not set up: a new version is a new download.
- **Windows.** The print path is the Mac's (`lp`).
- **A backup of the server.** The desk holds the register and the server holds 200 copies of it, but nothing copies the server's volume anywhere else.

## For contributors and coding agents

Read [`AGENTS.md`](AGENTS.md) first. It holds the physical facts (leaf and page sizes, the MICR band, the printer's unprintable margin), the geometry contract, the register's rules, and what must be verified after a change.

## Licence

Apache License 2.0, in [`LICENSE`](LICENSE). Copyright 2026 Banyan contributors. The software comes with no warranty: check every cheque before it is signed.

The bundled fonts are under the SIL Open Font License; their licence texts are in `static/fonts/`.
