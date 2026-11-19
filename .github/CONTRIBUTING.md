# Contributing

Thanks for taking a look. Banyan is a small app with one main user, a treasurer printing real cheques, so changes are judged by one question: could this put the wrong ink on a bank leaf, or lose a record?

## Before you start

- **Open an issue first** for anything bigger than a typo or an obvious bug, so we can agree on the change before you write it.
- Keep each pull request to **one change**. A small diff gets reviewed quickly.
- Change things where they already live. Don't add a second copy of a screen, a rule or the cheque geometry.

## Set up

You need macOS on Apple silicon, Go 1.26 and the Xcode command line tools.

```sh
sh scripts/build-mac.sh   # development build at dist/Banyan.app
go test ./...             # the desk against the recorded answers
```

The TypeScript (Electron) version is still in the tree as a reference; see the README for its `pnpm` commands.

## Rules that matter here

- **Never print during development.** Development builds and tests can't print, and that's on purpose. Don't switch it off to make something work.
- **No real data.** No account numbers, cheque numbers, MICR lines, names or registers in code, tests, issues or screenshots. Make the values up.
- **Money is integer paise.** Never use a float for an amount.
- **The register is append-only.** Cheques are voided, never deleted, and every state change writes an event row.
- **The recordings in `tests/golden/` are the spec.** If a test fails, fix the code. Edit a recording only when the behaviour change is the one you're proposing, and say so in the PR.
- **Cheque geometry is measured.** If you touch the layout, the print CSS, the template or the calibration, say so in the PR. That change also needs a plain-paper test print, which no automated check can replace.
- The frontend has no build step, framework or CDN assets. Keep it that way.

## Pull requests

- Make sure the tests pass.
- Describe what changed and why, and how you checked it.
- Add screenshots for UI changes, made with invented data.

## Security issues

Don't open a public issue. See [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your work is licensed under the [Apache-2.0 license](../LICENSE).
