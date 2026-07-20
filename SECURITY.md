# Security Policy

Rowtr sits in the path of your Claude Code traffic and holds a proxy auth token, so we
take its security posture seriously. This document describes how the product protects
you and how to report a problem.

## Supported versions

Rowtr is in **alpha**. Security fixes land on `main`; there is not yet a stream of
patched point releases. Always build or run from the latest `main`.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Instead, use GitHub's private vulnerability reporting:

> **Security → Report a vulnerability** on
> <https://github.com/9to5-worklife/rowtr/security/advisories>

Include:

- a description of the issue and its impact;
- steps to reproduce (a minimal proof-of-concept helps);
- affected version / commit (`rowtr version`) and platform.

We'll acknowledge your report, work with you on a fix, and credit you in the release
notes unless you'd prefer to stay anonymous. Please give us a reasonable window to
address the issue before any public disclosure.

## What Rowtr does to protect you

These are current, implemented protections — not aspirations:

- **Loopback-only binding.** `serve` refuses non-loopback listen addresses (including an
  empty host with just a port) and refuses cleartext `http://` non-local upstreams
  without an explicit `--unsafe-remote` flag, which also prints a warning.
- **Mutual authentication.** A 32-byte token is created `0600` in your config directory
  and reused across restarts. The health endpoint answers a challenge nonce with an
  HMAC-SHA256 proof, so `rowtr claude` will only trust — and hand traffic to — a proxy
  that actually holds the token file. Clients must present the token on `/v1/messages`
  (constant-time comparison; `401` otherwise), and the header is stripped before the
  request is forwarded upstream.
- **Log hygiene.** Prompt text never appears in `proxy.log` — only routing signals —
  unless you explicitly opt in with `ROWTR_DEBUG_INTENT=1`. The log is written `0600`
  and truncates once it passes 5 MB.
- **Explicit, persisted consent.** Local routing is opt-in. Rowtr asks once and persists
  your choice; a non-interactive or empty answer is treated as *no* — silence is never
  consent. Until you consent, the proxy runs in log-only **observe** mode.
- **No secret handling in code.** Rowtr reads Claude credentials from the environment /
  the Claude Code login and forwards them; it does not store or log them.
- **Bounded inspection.** Request-body inspection is capped (64 MB); oversized bodies are
  forwarded untouched rather than buffered.

## Scope notes

- Rowtr answers a *subset* of prompts locally; those never leave your machine. Everything
  that routes to the frontier is sent to Anthropic exactly as your client intended.
- Release binaries are currently **unsigned** and hand-distributed. Prefer building from
  source (`go build ./cmd/rowtr`) unless you received a zip directly from someone you
  trust. See *Trust & distribution* in the [README](README.md).
