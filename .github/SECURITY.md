# Security policy

Banyan keeps a financial register, prints onto real bank cheques, and syncs with a server that holds keys and tokens. Please report security problems privately.

## Reporting a vulnerability

Use GitHub's **[Report a vulnerability](https://github.com/satiyap/banyan/security/advisories/new)** form, found on this repository's Security tab. Please **don't** open a public issue or pull request for it.

Include:

- what the problem is and what someone could do with it
- steps to reproduce, or a proof of concept
- the version, or the commit, you tested

Use invented data only. Never send real cheque, account or register details.

This is a small project maintained by one person. You'll get a reply as soon as possible, usually within a week. Please give us a reasonable time to fix the problem before you disclose it.

## Supported versions

Only the latest release gets fixes.

## What's in scope

- The desktop app, including its local API, printing and register
- The Banyan server: keys, tokens, sync and the members' pages
- Anything that would let a read key write, or let a reader get the register file

Out of scope: problems that need someone who already controls the Mac running the desk or the server host.
