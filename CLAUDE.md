# CLAUDE.md — Rowtr

Project memory for Claude Code. Read this first in a new session to know where we left off.

## What Rowtr is

An **intelligent LLM router**. It sits in front of two model tiers and decides,
per request, which one handles it — so the expensive frontier model is only used
when a task genuinely needs frontier-grade reasoning.

- **Local tier ("Gatekeeper")** — cheap/fast, via **Ollama**. For summarize,
  define, classify, lookup, etc.
- **Frontier tier ("Expert")** — powerful/expensive, via **Anthropic Claude**
  (`claude-opus-4-8`). For reasoning, synthesis, and anything with tool use.
- **The Router** — the product's core. Today a free **keyword heuristic**; meant
  to get smarter later, but only once we can *measure* that a smarter router pays
  for its own added cost/latency.

## Guiding principles (agreed with the user)

1. **Measurement first.** Every path logs tier/latency/token-cost. We don't
   "smarten" the router until a scoreboard proves the current one is worth improving.
2. **The router's own cost is the central tension.** Keyword heuristic is free;
   an LLM-based router adds latency+cost that can eat the savings. Start free.
3. **Default to the cheap tier / to safety.** Ambiguous prompts → local. In the
   proxy, anything uncertain or tool-bearing → frontier. Never silently reroute in
   a way that corrupts metrics or breaks the client.

## Tech / conventions

- **Language: Go** (chosen over Rust — for an I/O-bound router the language speed
  is noise against model latency, and Go has official Anthropic + Ollama SDKs).
- Module path: `github.com/connorhoulihan/rowtr` (rename if repo owner changes).
- Anthropic: official `github.com/anthropics/anthropic-sdk-go`.
- Ollama: plain `net/http` to `/api/chat` (no heavy dep).
- **Working style: the USER runs shell commands (build/test/run) themselves.**
  Do NOT run `go build`/`go test`/`go run`/`ollama`/etc. unless asked — write the
  code and give them the commands.

## Architecture

```
cmd/rowtr/main.go     CLI: one-shot + `setup` + `serve` (proxy) + `score` (scoreboard)
cmd/rowtr-tray        macOS menu-bar app (separate binary, uses cgo via fyne.io/systray)
internal/router       Router interface, KeywordRouter; intent.go = Decide()/ExtractIntent()
internal/backend      Backend interface; Ollama (local) + Anthropic (frontier) impls
internal/pipeline     CLI path: route → check availability → dispatch → Result{metrics}
internal/proxy        Anthropic-compatible reverse proxy + local-offload; records usage
internal/usage        SQLite (pure-Go modernc.org/sqlite) usage store: Record()/Summary()
internal/eval         Slice 2 scoreboard: score the offload decision vs a labeled set
internal/setup        `rowtr setup`: sysinfo → model rec, Ollama/Claude checks, writes config
internal/sysinfo      host detection (RAM/arch) for the model recommendation
internal/config       defaults → config file → env; Path/UsagePath; price table
evals/router.jsonl    labeled eval cases (incl. the real misrouting cases from the log)
```

**Binaries:** `rowtr` (CLI/proxy, cgo-free) and `rowtr-tray` (menu-bar, needs cgo
+ builds on macOS). Deps to `go get`: `modernc.org/sqlite`, `fyne.io/systray`.
**Usage recording:** the proxy records local offloads with exact tokens + estimated
SavedUSD (frontier price × local token counts); frontier requests are recorded as
tier+model only (no token parse yet), so SpentUSD is incomplete — SavedUSD is the
honest headline number.

Key types: `router.Tier` (Local|Frontier), `router.Outcome{Tier,Reason,Intent,Internal,Offloadable}`,
`router.Decide(r, rawUserText, hasTools)` — the **single source of truth** both the
proxy and the scoreboard use. `router.ExtractIntent` strips Claude Code wrappers
(system-reminder/transcript/command-*) and flags agent-internal machinery (web
content, search sub-calls, suggestion mode) so those never offload.

## Two run modes

- **CLI:** `./rowtr "prompt"` — routes one prompt, prints answer + metrics.
- **Proxy:** `./rowtr serve [--mode observe|route]` — Anthropic-compatible endpoint.
  A client (Claude Code) points at it via `ANTHROPIC_BASE_URL=http://127.0.0.1:8787`.
  - `observe` (default): forward everything to Claude; log what WOULD route. Safe.
  - `route`: also divert Local + **tool-free** requests to Ollama, translating the
    reply into Anthropic's shape (streaming SSE + non-streaming). Full local
    completion runs BEFORE any bytes are written, so failures fall back to the
    frontier cleanly — the client never breaks.

Build/test (user runs these):
`go build -o rowtr ./cmd/rowtr` · `go vet ./...` · `go test ./...`

## Status (as of last session)

- **CLI:** done & verified — real local completions ran.
- **Proxy Phase 1 (observe) & Phase 2 (route/offload):** verified via curl — the
  non-streaming offload returned a valid Anthropic Message served by Ollama
  (`X-Rowtr-Tier: local`). SSE-streaming offload still wants a real-client check.
- **Intent-aware routing:** DONE. `router.Decide` now routes on the extracted
  human turn and refuses to offload agent-internal payloads. This fixed the log's
  misrouting (verbs matched inside fetched web pages / transcripts). Code written,
  **user to build & re-run** the proxy to confirm the log looks sane.
- **Slice 2 scoreboard:** DONE — `rowtr score` over `evals/router.jsonl`. Code
  written, user to run.
- **`rowtr setup`:** DONE (code written, user to run). Hardware check → model rec,
  Ollama + Claude checks, writes `~/.config/rowtr/config.json`. Flags: `--yes`,
  `--pull`, `--probe`.

## Product vision (user's stated goals, MVP 2/3)

The user wants Rowtr shippable as a download: `setup` (done), then a **usage
dashboard** and a **menu-bar/tray desktop app** showing local-vs-Claude savings.
Key insight: **all UI surfaces need a persistent usage store first** (every routed
request → tier, tokens, cost, and the counterfactual frontier cost = "savings").
Savings is an *estimate* (frontier price × local token counts). Decisions locked:
first build = setup (done); desktop app shape = **menu-bar/tray** (later).

- **Usage store:** DONE & verified — `internal/usage`, SQLite. Proxy records events.
- **Menu-bar/tray app:** DONE & verified on macOS — `cmd/rowtr-tray`. Headline is
  **usage conserved** (tokens/requests kept off the Claude quota + offload rate);
  $ saved is a demoted API-only line. This reframing came from the user: on a flat
  subscription, what matters is not running out of quota, not dollar cost.
- **`rowtr usage`:** DONE — cgo-free CLI that prints the same numbers. The
  cross-platform stats view (the tray is macOS-only; systray needs cgo built on
  the target). Windows/Linux friends use this.
- **Packaging:** DONE — `Makefile` (`build`/`app`/`dist`) makes a mac `.app` +
  zip; CLI cross-compiles (CGO_ENABLED=0) to windows/linux amd64+arm64. Artifacts
  in `dist/`. `packaging/` has Info.plist + QUICKSTART (mac) + QUICKSTART-cli
  (win/linux). Binaries are unsigned → Gatekeeper needs `xattr -dr com.apple...`.

## Verified in the audit session (2026-07-06)

- `rowtr score`: **100% (23/23)** on the regression set.
- `rowtr setup --yes`: full run — detected 16GB Apple Silicon, recommended +
  pulled **gemma3n:e4b** (7.5GB), found Claude Code login, wrote config. Fixed a
  UX bug it exposed: `--yes` no longer auto-triggers the multi-GB pull (explicit
  `--pull` or interactive yes required).
- **SSE streaming offload**: deliberately verified — correct 6-event Anthropic
  sequence served by gemma3n:e4b, resolved from the **config file** (no env var).
- **Frontier token accounting**: BUILT — `internal/proxy/accounting.go`
  (`frontierTap` via `rp.ModifyResponse`) parses SSE `message_start`/`message_delta`
  or JSON `usage`, records real frontier tokens + est. spend. `rowtr usage` and
  the tray now show "Spent on Claude" and a %-of-tokens offload rate.
  **Not yet exercised against real authenticated Claude traffic** — needs the
  user to run a session through the proxy.
- **Release packaging**: `make release` → stripped (`-s -w -trimpath`), versioned
  (`--version` → 0.1.0), LICENSE.txt (proprietary eval license) in every zip,
  SHA256SUMS.txt. Zips halved in size (mac 7.9MB, linux ~5MB).

## Distribution & source-protection decisions

- Go binaries don't expose source; strip flags remove symbols/paths. Real
  protection = private repo + binaries-only distribution + eval LICENSE.
- Path when ready for testers beyond friends: private GitHub repo + GoReleaser
  (builds, checksums, Releases, Homebrew tap). Signing/notarization later.
- NOTE: the project is **not a git repo yet** — init + private remote is a
  sensible next step before wider distribution.

## Ollama bootstrap (added after "what if a user has no Ollama?")

`rowtr setup` is now a **one-command bootstrap on a bare machine**: if Ollama is
missing it offers to install it (macOS: brew, else download page; Linux: official
install script; Windows: winget), starts the daemon detached (`open -a Ollama` or
`ollama serve`), waits up to 15s, then proceeds to model recommendation/pull.
Fully hands-off: `rowtr setup --yes --pull`. Design decision: **models are never
bundled in the zip** — multi-GB + weight-license issues (Gemma/Llama terms);
setup orchestrates the pull instead. Future tiers if wanted: bundle the MIT-
licensed Ollama binary as a managed child process; or embed llama.cpp directly.
⚠️ The install paths (brew/winget/linux script) are UNTESTED — this machine
already has Ollama; needs a bare machine (e.g. the friend's Windows box).

## `rowtr claude` launcher (added & verified)

Users never type `ANTHROPIC_BASE_URL=... claude` — **`rowtr claude`** is the
entrypoint: checks `/rowtr/health` on `cfg.ProxyAddr` (default 127.0.0.1:8787,
persisted in config as `proxy_addr`, env `ROWTR_PROXY_ADDR`); if no Rowtr proxy,
re-execs itself as `serve --mode route` detached (log → config dir/proxy.log),
waits for health, then spawns `claude` with the env var injected, stdio wired,
args passed through, exit code propagated. If the port is busy with a non-Rowtr
service it errors with guidance instead of fighting for the port. Verified both
paths (autostart + reuse) via `rowtr claude --version`. The proxy deliberately
outlives claude (feeds the tray). All docs/quickstarts now lead with `rowtr claude`.

## Repo, CI, and package testing (all verified 2026-07-06)

- **Private GitHub repo:** https://github.com/chouli12/rowtr (user: chouli12).
  Clean initial commit, binaries gitignored (`dist/`, `bin/`, stray `cmd/rowtr/rowtr`).
- **CI:** `.github/workflows/smoke.yml` — on push, builds + unit-tests + smoke-tests
  (version, usage store, proxy health) on **windows-latest / macos-latest /
  ubuntu-latest**. First run: ALL THREE GREEN, incl. real Windows (AppData paths,
  proxy bind, health OK).
- **Package testing:** `scripts/smoke.sh` runs against a literal release zip;
  both Linux zips PASS in Docker (Alpine arm64 native + amd64 via QEMU).
  Docker CANNOT test Windows from a Mac — that's what the Actions matrix is for.
  Caveat: CI tests source-built binaries, not the literal cross-compiled zips;
  GoReleaser later can close that gap.
- Naming note for the user: **x64 = amd64 = x86-64** — the windows-amd64 zip is
  correct for any 64-bit Intel/AMD Windows machine.
- Ship flow decided: private repo for source; hand the zip directly to 1–2
  friends; GitHub Releases (+GoReleaser, public releases-only repo) at ~5+ testers.

## "Downloadable and just works" state (2026-07-06, late session)

- User ran real traffic through the proxy: 83% offload rate, 1700 tok kept off
  quota. Frontier accounting recorded the request but **model/tokens were empty →
  gzip**: Claude Code sends Accept-Encoding, tap saw compressed bytes. FIXED:
  Director now strips Accept-Encoding (Go transport transparently de-gzips) +
  tap skips any still-encoded body. Release assets refreshed with the fix.
  **User must restart their proxy** (`pkill rowtr; rowtr claude`) to pick it up;
  then re-verify `rowtr usage` shows frontier tokens.
- **`scripts/install.sh`** written & tested end-to-end against a local HTTP
  server: OS/arch detect → download zip → install to PATH (+ Rowtr.app to
  ~/Applications on mac). curl downloads carry no quarantine flag → no
  Gatekeeper wall. Overrides: ROWTR_VERSION / ROWTR_INSTALL_DIR / ROWTR_BASE_URL.
- **Why a raw download doesn't "just work" today:** (1) release lives on the
  private repo — no public URL; (2) unsigned binaries → browser downloads hit
  Gatekeeper/SmartScreen; (3) no PATH install from a bare zip. The install
  script solves 2+3; solving 1 needs the public releases-only repo.
- **DECISION (user): keep everything private for now** — no public releases repo
  yet; keep hand-sending zips. The installer script points at
  github.com/chouli12/rowtr-releases and activates the moment that public repo
  is created + release uploaded. Real signing/notarization remains the eventual
  fix for browser downloads.

## Roadmap / next steps

1. **Re-verify frontier accounting** after the user restarts the proxy (gzip fix).
1b. **Test the Ollama bootstrap on a bare machine** (friend's Windows/Linux box).
1c. **When ready to go public:** create public `rowtr-releases` repo, upload the
    zips + `scripts/install.sh`, and the curl one-liner goes live unchanged.
2. **Tray polish:** proxy start/stop from the tray; a menu-bar icon; time windows
   (today / all-time).
3. **Cross-platform tray:** needs cgo built on target — per-OS builds or CI.
4. **git init + private GitHub + GoReleaser** when distributing beyond friends;
   signing/notarization after that.
5. **Slice 3 — the cascade (FrugalGPT-style):** run local first, score the output,
   escalate to frontier only if weak. Needs a cheap quality judge.

## Design notes captured

- **Don't route by workflow phase** (plan vs implement). Implementation in Claude
  Code is a tool-use loop → frontier. Local's sweet spot is small, self-contained,
  tool-free snippets (regex, JSON→struct, boilerplate, commit msgs). Route by
  self-containedness + tool need, not phase.
- Signals worth adding beyond keywords: context size, output-rigor (schema), and
  above all the **cascade** (try-local-then-judge).

## Environment notes (this machine)

- Ollama at `/usr/local/bin/ollama`. User has a truly-local model **gemma3n:e2b**
  (they wrote "gemma4e2b"; confirm exact tag via `ollama list`). Default local
  model is now `gemma3n:e2b`; override via `ROWTR_LOCAL_MODEL`. There is also a
  `nemotron-3-super:cloud` (cloud-proxied, slow, was timing out — avoid).
- `ANTHROPIC_API_KEY` not set last session — frontier paths need it.
- A Rust toolchain was installed early on but is unused (we chose Go).
