# Rowtr — quick start (Windows / Linux)

Rowtr routes your Claude Code prompts between a **local** model (free, runs on
your machine) and **Claude** (the frontier model). Simple, tool-free prompts get
handled locally so you don't burn your Claude usage on them — your subscription
lasts longer. Everything that needs Claude still goes to Claude.

> The graphical menu-bar app is macOS-only for now. On Windows/Linux you get the
> same numbers from the command line: `rowtr usage`.

## Prerequisites

Just **Claude access** — a Claude Code login (subscription) or an
`ANTHROPIC_API_KEY`. If you use Claude Code normally, you're set.

Don't have Ollama or a local model? `rowtr setup` detects what's missing, offers
to install Ollama (winget on Windows, the official installer script on Linux),
starts it, and downloads a model sized to your machine.

## Run

Linux shell:
```
./rowtr setup                 # checks machine, installs Ollama if missing, recommends a model
./rowtr claude                # starts the Rowtr proxy AND Claude Code, already connected
```
Windows (PowerShell):
```
.\rowtr.exe setup
.\rowtr.exe claude
```
Fully hands-off bootstrap (installs Ollama AND downloads the model, several GB):
```
rowtr setup --yes --pull
```

That's it — `rowtr claude` replaces the `claude` command. Any arguments pass
through (`rowtr claude --resume`). The proxy keeps running in the background
after Claude Code exits.

Use Claude Code normally. See how much you've kept off your Claude quota:
```
./rowtr usage        # (Windows: .\rowtr.exe usage)
```

## Notes

- `--mode observe` (instead of `route`) forwards everything to Claude and only
  logs what it *would* route — a safe way to see your traffic first.
- On coding work, most turns still go to Claude (they need tools) — the wins show
  up on simple, tool-free questions.
- Config and usage live in your OS config dir (`~/.config/rowtr` on Linux,
  `%AppData%\rowtr` on Windows).
- Early test software — expect rough edges, and tell Chuck what breaks.
