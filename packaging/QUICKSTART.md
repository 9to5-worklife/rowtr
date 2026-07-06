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
./rowtr setup            # checks machine, installs Ollama if missing, recommends a model
# fully hands-off (installs Ollama AND downloads the model, several GB):
./rowtr setup --yes --pull

./rowtr claude           # starts the Rowtr proxy AND Claude Code, already connected
```

That's it — `rowtr claude` replaces the `claude` command. Any arguments pass
through (`./rowtr claude --resume`). The proxy keeps running in the background
after Claude Code exits.

Open **Rowtr.app** (it appears in your menu bar, top-right) to watch how many
tokens/requests were kept off your Claude quota. On coding work most turns still
go to Claude (they need tools) — the wins show up on simple, tool-free questions.

## Notes

- `--mode observe` (instead of `route`) forwards everything to Claude and only
  logs what it *would* route — a safe way to see your traffic first.
- Config and usage live in `~/Library/Application Support/rowtr/`.
- This is early test software — expect rough edges, and tell Chuck what breaks.
