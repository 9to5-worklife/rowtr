# Rowtr — quick start

Rowtr routes your Claude Code prompts between a **local** model (free, runs on
your machine) and **Claude** (the frontier model). Simple, tool-free prompts get
handled locally so you don't burn your Claude usage on them — your subscription
lasts longer. Everything that needs Claude still goes to Claude.

This folder contains:
- `rowtr` — the command-line tool + proxy
- `Rowtr.app` — the menu-bar app showing usage kept off your Claude quota

## Prerequisites

Just **Claude access** — a Claude Code login (subscription) or an
`ANTHROPIC_API_KEY`. If you use Claude Code normally, you're set.

Don't have Ollama or a local model? You don't need to install anything first —
`rowtr setup` detects what's missing, offers to install Ollama, starts it, and
downloads a model sized to your machine.

## First run

macOS will flag these as "unidentified developer" (they're unsigned). Clear that
once:
```
xattr -dr com.apple.quarantine .
```

Then:
```
./rowtr setup            # checks machine, recommends a local model
# fully hands-off (installs Ollama AND downloads the model, several GB):
./rowtr setup --yes --install --pull

./rowtr claude           # starts the Rowtr proxy AND Claude Code, already connected
```
(`--yes` alone never installs software or downloads models — those need
`--install` / `--pull` or an interactive yes.)

That's it — `rowtr claude` replaces the `claude` command. Any arguments pass
through (`./rowtr claude --resume`). The proxy keeps running in the background
after Claude Code exits.

The first `rowtr claude` asks whether to route eligible prompts to your local
model; until you say yes it runs in **observe** mode (everything goes to Claude,
routing is only logged). Change your answer any time with `rowtr claude --route`
/ `--observe`. When a session ends, Rowtr prints what was answered locally.

Open **Rowtr.app** (it appears in your menu bar, top-right) to watch how many
tokens/requests were kept off your Claude quota. On coding work most turns still
go to Claude (they need tools) — the wins show up on simple, tool-free questions.

## Notes

- Config and usage live in `~/Library/Application Support/rowtr/`.
- The proxy only accepts requests from clients started by `rowtr claude` (a
  local auth token, generated on first start). Running `rowtr serve` by hand
  prints the header to copy for other clients, or use `--no-auth`.
- This is early test software — expect rough edges, and tell Chuck what breaks.
