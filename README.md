# Rowtr

An intelligent LLM router. Rowtr sits in front of two model tiers and decides,
per request, which one should handle it — so you only pay the frontier-model
"tax" when a task actually needs frontier-grade reasoning.

- **Local (Gatekeeper)** — cheap/fast, via [Ollama](https://ollama.com). Handles
  the high-volume, low-complexity majority (summarize, define, classify, lookup).
- **Frontier (Expert)** — powerful/expensive, via Anthropic's Claude. Reserved
  for genuine reasoning, synthesis, tool use, and open-ended work.
- **The Router** — inspects each prompt and picks a tier. Today it's a free
  keyword heuristic; it gets smarter later, once we can *measure* that it helps.

---

## Getting started

### 1. Prerequisites

- **Go 1.24+** — `go version`
- **Ollama** (for the local tier) — install, start it, and pull a model:
  ```bash
  brew install ollama
  ollama serve            # leave running (or use the Ollama.app)
  ollama pull llama3.2    # the default local model
  ```
- **Anthropic API key** (for the frontier tier):
  ```bash
  export ANTHROPIC_API_KEY=sk-ant-...
  ```

Rowtr runs even if a backend is missing — it reports which one and how to fix it,
and never silently reroutes (that would corrupt the metrics).

### 2. Build

```bash
cd /Users/chuck/Code/Rowtr
go build -o rowtr ./cmd/rowtr      # produces ./rowtr — rebuild after code changes
```

### 3. Run setup (recommended)

```bash
./rowtr setup            # add --probe to test your Claude key, --pull to fetch the model
```

`setup` checks your hardware and **recommends a local model** sized to your RAM,
verifies Ollama is running, checks for working Claude access, and writes a config
file (`~/Library/Application Support/rowtr/config.json` on macOS, `~/.config/rowtr/`
on Linux) so you don't need env vars. Flags: `--yes`
(non-interactive), `--pull` (download the recommended model), `--probe` (make a
tiny Claude call to confirm credentials work).

> It can confirm Claude credentials *exist and work*, but can't tell a
> subscription from an API key or report the tier. If you use Claude Code's login,
> the proxy forwards it — no separate key needed.

### 4. Configuration (env vars override the config file; all optional)

| Var | Default | Meaning |
| --- | --- | --- |
| `ROWTR_LOCAL_MODEL` | `gemma3n:e2b` | Ollama model for the local tier |
| `ROWTR_FRONTIER_MODEL` | `claude-opus-4-8` | Claude model for the frontier tier |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama daemon URL |
| `ANTHROPIC_API_KEY` | — | Anthropic key (read by the SDK) |

> Confirm your exact local model tag with `ollama list` and override
> `ROWTR_LOCAL_MODEL` if it differs from the default.

---

## Two ways to run it

### A) CLI — one prompt at a time

```bash
./rowtr "define entropy"                 # → routed local
./rowtr "compare REST and gRPC"          # → routed frontier (needs API key)
./rowtr --tier local "list 3 colors"     # force a tier
./rowtr --json "summarize DNS"           # machine-readable output
echo "translate hello to French" | ./rowtr
```

The answer prints to **stdout**; the routing line (tier · reason · model ·
latency · tokens · cost) prints to **stderr**.

### B) Proxy — transparently in front of Claude Code

The one-command way — starts the proxy (if not already running) and launches
Claude Code already connected to it; extra args pass through:

```bash
./rowtr claude                   # instead of `claude`
./rowtr claude --resume          # args pass through
```

Manual control, if you want to run the pieces yourself:

```bash
./rowtr serve                    # observe mode (default): forward all, log routing
./rowtr serve --mode route       # route mode: divert Local + tool-free requests to Ollama
# separate terminal:
ANTHROPIC_BASE_URL=http://127.0.0.1:8787 claude
```

- **observe** — 100% safe. Everything still goes to Claude; Rowtr just logs what
  tier each prompt *would* route to. Use this to measure real traffic first.
- **route** — actually offloads: requests the router marks Local *and* that carry
  no tools go to Ollama; everything else (including all tool-using turns) still
  goes to Claude. If the local tier can't serve a request for any reason, Rowtr
  falls back to the frontier — the client never breaks.

> Note: `ANTHROPIC_BASE_URL` is read at startup, so this can't reroute an
> already-running session — launch a new client to test. Don't point your primary
> Claude Code at `--mode route` until you've watched it behave in `observe` first.
> Locally-served turns are tagged with `X-Rowtr-Tier: local` response headers.

---

### C) Scoreboard — measure the routing

The router decides per request whether to offload to local. `rowtr score` runs it
over a labeled eval set and reports how often that decision is right:

```bash
./rowtr score                          # uses evals/router.jsonl
./rowtr score --evals path/to/set.jsonl
```

It prints accuracy plus a confusion breakdown — **FP** = wrongly sent to local (a
quality risk), **FN** = a missed saving — and lists every mismatch with the reason,
so failures are actionable. Grow `evals/router.jsonl` from real traffic and treat
the accuracy as the number to beat before making the router fancier.

> **Intent-aware routing:** Rowtr routes on the *human's* prompt, not the raw last
> message. It strips Claude Code's injected wrappers (`<system-reminder>`,
> `<transcript>`, slash-command tags) and flags agent-internal machinery (fetched
> web pages, "Perform a web search…", suggestion mode) so those are never offloaded
> — even if they contain routing keywords.

### D) Menu-bar app — see your savings

A macOS menu-bar app shows how much has been saved by serving requests locally,
with a per-model breakdown, reading the usage the proxy records.

```bash
# one-time: fetch the two extra deps used by the store + tray
go get modernc.org/sqlite fyne.io/systray && go mod tidy

# build the tray (separate binary; uses native GUI APIs, so build on macOS)
go build -o rowtr-tray ./cmd/rowtr-tray
./rowtr-tray
```

Run `rowtr serve` (which records usage to the config dir — `~/Library/Application
Support/rowtr/usage.db` on macOS), drive some traffic, and the menu bar shows
`$X.XX saved`; the dropdown lists local vs Claude request counts and a per-model
breakdown.

> **What's exact vs estimated:** local offloads are recorded with real token counts,
> and **saved $** is an estimate (Claude's price × those tokens). Frontier requests
> are recorded as tier+model only (no token parsing yet), so "spent" isn't tracked —
> saved is the honest headline number.

## Layout

```
cmd/rowtr        CLI (one-shot + `setup` + `serve` proxy + `score` scoreboard)
cmd/rowtr-tray   macOS menu-bar app showing saved-$ + per-model usage (cgo)
internal/router  Router interface + keyword heuristic; Decide()/ExtractIntent()
internal/backend Backend interface + Ollama and Anthropic implementations
internal/pipeline route → dispatch → assemble Result with metrics (CLI path)
internal/proxy   Anthropic-compatible reverse proxy + local-offload; records usage
internal/usage   SQLite usage store (pure-Go modernc.org/sqlite)
internal/eval    scoreboard: score the offload decision against a labeled set
internal/setup   first-run system check, model recommendation, config file
internal/sysinfo host detection (RAM/arch) for the model recommendation
internal/config  defaults → config file → env; price table
evals/           labeled eval cases (router.jsonl)
```

## Develop

```bash
go build ./...
go vet ./...
go test ./...   # router decisions are covered by table-driven tests
```
