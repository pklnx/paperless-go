# Security Policy

## Reporting a vulnerability

The repository is private, so only its collaborators can read its issues.
Report security problems as an issue with the label `security`.

Include what you found, how to reproduce it and what an attacker could do with it.

## Supported versions

Only the latest state of the `main` branch is supported. There are no
maintained older versions.

## What to expect

This is a hobby project maintained in spare time. Reports are handled on a
best-effort basis, without fixed response times. You will be credited in the
fix unless you prefer otherwise.

This project is entirely AI-written (see the [README](README.md)).
Independent review is welcome.

## Scope

In scope, for example:

- Bypassing the Host and same-origin checks of the web server
- Reading or changing documents without access (once user accounts exist:
  bypassing login, sessions, TOTP, passkeys or API keys, or reaching another
  user's documents)
- Path traversal or code execution through uploaded files (PDFs, images)
- Sending document contents to an external service (Mistral) without it
  being turned on

Out of scope:

- Setups that go against the
  [security documentation](https://pklnx.github.io/paperless-go/reference/security),
  for example exposing the web UI to the internet or running it without HTTPS
- Unencrypted document files on disk (documented: use an encrypted disk)
- Vulnerabilities in dependencies without a demonstrated impact on this project
