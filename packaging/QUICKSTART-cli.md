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

## Windows: first run

Windows will warn about an unsigned downloaded exe — that's expected for now
(the binaries aren't code-signed yet):

1. If SmartScreen shows "Windows protected your PC": click **More info → Run
   anyway**. (Or beforehand: right-click `rowtr.exe` → Properties → check
   **Unblock**, or in PowerShell: `Unblock-File .\rowtr.exe`.)
2. **Double-clicking `rowtr.exe` works** — it opens a window that walks you
   through first-time setup. Day-to-day use is from PowerShell, though:

```
cd <folder where you unzipped rowtr>
.\rowtr.exe setup
.\rowtr.exe claude
```

## Run

Linux shell:
```
./rowtr setup                 # checks the machine, recommends a local model
./rowtr claude                # starts the Rowtr proxy AND Claude Code, already connected
```
Fully hands-off bootstrap (installs Ollama AND downloads the model, several GB):
```
rowtr setup --yes --install --pull
```
(`--yes` alone never installs software or downloads models — those need
`--install` / `--pull` or an interactive yes.)

That's it — `rowtr claude` replaces the `claude` command. Any arguments pass
through (`rowtr claude --resume`). The proxy keeps running in the background
after Claude Code exits.

The first `rowtr claude` asks whether to route eligible prompts to your local
model. Until you say yes it runs in **observe** mode — everything goes to
Claude and Rowtr only logs what it *would* have routed. Change your answer any
time with `rowtr claude --route` / `rowtr claude --observe`. When a session
ends, Rowtr prints what (if anything) was answered locally.

Use Claude Code normally. See how much you've kept off your Claude quota:
```
./rowtr usage        # (Windows: .\rowtr.exe usage)
```

## Notes

- On coding work, most turns still go to Claude (they need tools) — the wins show
  up on simple, tool-free questions.
- The proxy only accepts requests from clients started by `rowtr claude` (a
  local auth token, generated on first start). Running `rowtr serve` by hand
  prints the header to copy for other clients, or use `--no-auth`.
- Config and usage live in your OS config dir (`~/.config/rowtr` on Linux,
  `%AppData%\rowtr` on Windows).
- Early test software — expect rough edges, and tell Chuck what breaks.
