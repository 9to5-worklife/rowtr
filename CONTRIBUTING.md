# Contributing to Rowtr

Thanks for your interest in Rowtr! This is an early-stage project, and thoughtful
contributions — bug reports, eval cases, docs, and code — are all welcome.

By contributing you agree that your contributions are licensed under the project's
[PolyForm Noncommercial 1.0.0](LICENSE) license.

## Ground rules

Rowtr has a few load-bearing principles. Please keep them in mind — a change that
violates one is unlikely to be merged even if it works:

1. **Measurement first.** Every routing path logs tier, latency, and token cost. Don't
   make the router "smarter" without a way to *measure* that it helped. The scoreboard
   (`rowtr score`) is the arbiter.
2. **Default to the cheap tier / to safety.** Ambiguous prompts route local; anything
   uncertain or tool-bearing goes to the frontier. Never silently reroute in a way that
   corrupts metrics or breaks the client.
3. **Never break the client.** A local completion must fully succeed *before* any bytes
   are written to the client, so a failure falls back to Claude cleanly. Real
   conversation turns are forwarded byte-identical — only allowlisted internal
   housekeeping is ever mutated or replayed.

## Development setup

Prerequisites: **Go 1.26+**, and [Ollama](https://ollama.com) if you want to exercise
the local tier.

```bash
git clone https://github.com/9to5-worklife/rowtr && cd rowtr
go build ./...
go vet ./...
go test ./...
```

The tray app (`cmd/rowtr-tray`) needs cgo and builds on macOS:

```bash
go build -o rowtr-tray ./cmd/rowtr-tray
```

## Making a change

1. **Open an issue first** for anything non-trivial, so we can agree on the approach
   before you invest time.
2. **Branch** off `main` and keep the change focused — one logical thing per PR. Don't
   refactor unrelated code in the same diff.
3. **Match the surrounding code.** Consistency with existing idioms beats personal
   preference. Names carry intent; comments explain *why*, not *what*.
4. **Add tests.** Router decisions are table-driven — a behavior change should show up as
   a test change. A bug fix starts with a failing test that reproduces it.
5. **Run the full check** before pushing:
   ```bash
   go build ./... && go vet ./... && go test ./...
   ```
6. **Open a PR against `main`.** `main` is protected and requires a green CI run
   (build + unit tests + smoke tests on macOS, Linux, and Windows).

## Contributing eval cases

The single most valuable non-code contribution is **real routing cases**. If you find a
prompt that Rowtr routes wrongly, add it to `evals/router.jsonl` with the correct label
and a short note, and confirm `rowtr score` reflects it. The eval set is how we keep the
router honest as it evolves.

## Commit messages

Write a concise summary line in the imperative mood ("Add cache-aware accounting"),
followed by a body explaining the *why* when it isn't obvious from the diff.

## Reporting bugs

Open a [GitHub issue](https://github.com/9to5-worklife/rowtr/issues) with:

- what you ran (command + mode), and what you expected vs. saw;
- OS and `rowtr version`;
- relevant routing-log lines (**scrub any prompt text first** — and note that
  `proxy.log` never contains prompt text unless you set `ROWTR_DEBUG_INTENT=1`).

For **security** issues, do **not** open a public issue — see [SECURITY.md](SECURITY.md).
