# RowtrKit — iOS routing SDK (design spec)

Status: **draft / design only — no code yet.** Target: a Swift package that brings
Rowtr's per-prompt routing to iOS apps that call Claude.

> **What transfers is the *decision*, not the plumbing.** Rowtr-desktop's value is
> `router.Decide()` — "does this prompt actually need the frontier?" — plus the
> measurement of what routing conserved. The Ollama/local-proxy machinery does *not*
> transfer to a phone. RowtrKit reuses the IP (routing + measurement) and swaps the
> cheap tier for Apple's **on-device Foundation Models**.

---

## 1. Goal & scope

**Goal.** Let an iOS app that already calls Claude answer its *easy* prompts on the
device's built-in model, keeping Claude for the hard ones — with the same honest
"what did we conserve?" measurement Rowtr-desktop provides.

**In scope (v1):**
- A drop-in async API shaped like an Anthropic Messages call.
- A default routing policy (ported keyword + signal heuristics).
- On-device tier via Apple Foundation Models, with graceful fallback.
- Frontier tier via the Anthropic Messages API (streaming + non-streaming).
- Local measurement: tier, tokens, estimated conserved cost.

**Out of scope (v1):**
- Android (mirrors this later via ML Kit GenAI / Gemini Nano).
- A hosted gateway (separate product; build last).
- Agentic tool loops — a tool-bearing turn always routes to Claude, same as desktop.
- On-device models other than Apple's (no llama.cpp/MLC in v1 — battery/thermal make
  a bundled SLM the wrong default; see §9).

**Non-goal:** RowtrKit never claims to improve Claude's answers. It *conserves* them.

---

## 2. Where this differs from Rowtr-desktop

| Concern | Desktop (Go proxy) | RowtrKit (iOS) |
|---|---|---|
| Cheap tier | Ollama on the user's machine | Apple on-device Foundation Model |
| Integration | Transparent proxy (`ANTHROPIC_BASE_URL`) | In-process SDK call |
| Input | Claude Code requests full of agent wrappers | The app's own clean prompts |
| Intent extraction | Strips `<system-reminder>`, transcripts, etc. | **Mostly unneeded** — the app's prompt *is* the intent |
| Frontier auth | Forwards the user's Claude Code login | The app's API key (see §8 security) |
| Measurement | SQLite `usage.Store` | In-memory + optional on-disk store |

The single most important adaptation: **on mobile there are no Claude Code wrappers to
strip**, so the internal-payload/allowlist machinery (`internalMarkers`,
`classifyIntent`) is largely irrelevant. RowtrKit routes on the raw prompt the app
gives it. The signals that *do* transfer are the useful ones: keyword heuristic,
prompt length, tool-need, and structured-output demand.

---

## 3. Architecture

```
┌───────────────────────────────────────────────┐
│  App code                                      │
│    let reply = try await rowtr.message(...)     │
└───────────────────────┬────────────────────────┘
                        │
                ┌───────▼────────┐
                │  Rowtr (facade) │   decide → dispatch → record
                └──┬──────────┬───┘
       decide()    │          │   record()
        ┌──────────▼──┐   ┌───▼─────────┐
        │ RoutingPolicy│   │ UsageStore  │  (in-memory + optional file)
        └──────┬───────┘   └─────────────┘
               │ Route(.local / .frontier)
        ┌──────▼───────────────────────────┐
        │            Dispatcher             │
        └──┬───────────────────────────┬────┘
     .local│                    .frontier│
   ┌───────▼────────┐          ┌─────────▼──────────┐
   │ OnDeviceBackend │  fail→   │  FrontierBackend    │
   │ (FoundationModels)│───────▶│  (Anthropic API)    │
   └─────────────────┘  fallback└────────────────────┘
```

Four protocols, so every piece is swappable and testable (mirrors the Go
`Router`/`Backend` interfaces):

```swift
protocol RoutingPolicy { func decide(_ req: Request) -> Route }
protocol Backend {
    var tier: Tier { get }
    func complete(_ req: Request) async throws -> Reply
    func stream(_ req: Request) -> AsyncThrowingStream<Chunk, Error>
    var isAvailable: Bool { get }
}
```

---

## 4. Public API (illustrative Swift — names to be finalized)

```swift
import RowtrKit

// Configure once (e.g. at app launch).
let rowtr = Rowtr(
    frontier: .anthropic(apiKey: key, model: "claude-opus-4-8"),
    policy: .default,          // or a custom RoutingPolicy
    mode: .observe             // .observe (log only) until the app opts in — same
                               // safe default as desktop
)

// Drop-in call — same shape an Anthropic message takes.
let reply = try await rowtr.message(
    system: "You are a concise assistant.",
    messages: [.user("Summarize this in one line: \(article)")]
)
reply.text            // the answer
reply.tier            // .local or .frontier — which tier served it
reply.usage           // inputTokens, outputTokens, estConservedUSD

// Streaming.
for try await chunk in rowtr.stream(messages: msgs) {
    render(chunk.text)
}

// Measurement, for the app to surface.
let s = rowtr.usage.summary()
// s.requestsKeptLocal, s.tokensKeptLocal, s.offloadRate, s.estSavedUSD (labeled est.)
```

**Design choices**
- `mode: .observe` is the default — RowtrKit logs what it *would* route until the app
  flips to `.route`. Same "silence isn't consent" posture as desktop.
- The call signature intentionally mirrors an Anthropic message so adoption is a
  near-drop-in: replace the Anthropic client call with `rowtr.message(...)`.
- `reply.tier` is always exposed — the app (and the user) can always see what happened.

---

## 5. Routing policy (the ported IP)

`DefaultPolicy` ports the useful half of `internal/router`:

- **Keyword heuristic** — port `frontierVerbs` / `localVerbs` and the "frontier verb >
  local verb > long-prompt > default-local" ladder from `keyword.go`.
- **Length signal** — long prompts (> ~60 words) lean frontier (already in Go).
- **Tool-need** — if the app declares tools for a turn, force frontier (no on-device
  tool loop in v1).
- **Structured-output demand** — a strict JSON/schema request is a *good* local
  candidate when it's extraction, *bad* when it's reasoning; combine with length.
- **Default to safety** — anything ambiguous → frontier.

Deliberately **not** ported: `classifyIntent` / `internalMarkers` / wrapper stripping —
those exist to undo Claude Code's payloads and have no analog in a normal app.

The policy is a protocol, so an app can supply its own, and later we can drop in the
`generalization.jsonl`-trained classifier from the desktop measurement work.

---

## 6. On-device tier — Apple Foundation Models

> **Verify against current Apple docs before coding** — API names below are indicative
> (Foundation Models framework, WWDC 2025/2026). Availability, model size, and the
> provider-protocol details must be confirmed.

- **Availability gate.** Foundation Models is only on Apple-Intelligence-capable
  devices. Check availability at runtime; when unavailable, the on-device backend
  reports `isAvailable == false` and the dispatcher routes everything to frontier.
  ```swift
  guard SystemLanguageModel.default.availability == .available else { /* frontier */ }
  ```
- **What it's good for** (short, self-contained tasks — exactly Rowtr's sweet spot):
  summarize, classify, rewrite, extract, title, one-liners. **Not** long reasoning,
  synthesis, or tool use.
- **Structured output.** Foundation Models' guided generation (`@Generable`-style) is a
  strong fit for the extraction/classification cases — worth using where the app wants
  typed results.
- **Cost/latency/battery.** The OS model is free, managed (no download), and
  battery-sane for *short* tasks. Long or bulk on-device inference is not — so the
  policy must keep on-device work to short prompts (a max-input guard, like the
  desktop `maxCascadeBody`).

**Token accounting caveat:** on-device calls have no per-token billing, so "tokens kept
local" is measured by counting, and "estimated conserved $" = Claude's price × those
counts — an **estimate**, labeled as one, exactly as desktop does it.

---

## 7. Frontier tier & fallback

- **FrontierBackend** calls the Anthropic Messages API (via the official Swift SDK if
  suitable, else `URLSession`), supporting streaming.
- **Fallback is sacred** (the desktop invariant): the on-device completion must fully
  succeed before RowtrKit returns it; any on-device error, unavailability, or
  timeout falls through to the frontier. The app never sees a broken result because
  routing was attempted.
- **Cascade (later).** The desktop cascade (try-local-then-judge) can port directly:
  run on-device, judge with a cheap Claude (Haiku) call, escalate if weak. Gate it
  behind measurement, same as desktop — **off in v1.**

---

## 8. Security & privacy

- **On-device prompts never leave the device.** This is a real, checkable claim (unlike
  routing to a third-party cloud model) and a headline selling point — but scope it
  honestly: only the *offloaded subset* is local.
- **API key handling is the app's biggest risk, not RowtrKit's.** Embedding an Anthropic
  key in a shipped app is unsafe. RowtrKit should support a **proxied frontier
  endpoint** (the app's own backend, or a future hosted Rowtr gateway) as the
  recommended production path, and document the raw-key mode as dev-only.
- No prompt text in any RowtrKit logs by default (mirror `ROWTR_DEBUG_INTENT`).

---

## 9. Why not bundle a small model (llama.cpp / MLC)?

On a modern iPhone a bundled 3B model runs at single-digit tokens/sec, drains ~10%
battery per ~20 inferences, and thermally throttles under sustained load. That's the
wrong default for a routing tier that fires constantly. Apple's OS model is free,
shared, and battery-managed. **v1 uses the OS model only;** a bring-your-own-model
backend can be added later for niche offline apps, behind the same `Backend` protocol.

---

## 10. Packaging & milestones

- **Distribution:** Swift Package Manager. Separate repo (`rowtr-swift` /
  `RowtrKit`), or a `swift/` subdir — decide before scaffolding.
- **Min OS:** whatever Foundation Models requires (recent iOS); the frontier-only path
  can support older iOS so the SDK degrades gracefully.
- **License:** same PolyForm Noncommercial for the OSS SDK; a commercial license for
  apps embedding it commercially (the built-in dual-license lever).

**Build order:**
1. **M1 — routing skeleton:** `Rowtr` facade + `DefaultPolicy` + `FrontierBackend`
   (Anthropic) + `OnDeviceBackend` (Foundation Models) + fallback. Non-streaming first.
2. **M2 — measurement:** `UsageStore` + `summary()` + a sample SwiftUI view that shows
   "tokens kept local / offload rate."
3. **M3 — streaming + guided generation** for the extraction cases.
4. **M4 — Android mirror** (ML Kit GenAI / Gemini Nano) behind the same protocols.
5. **Later — cascade + trained policy**, gated on measurement.

---

## 11. Open questions / risks

- **Apple API surface & availability** — confirm the exact Foundation Models API,
  minimum device/OS, and the WWDC 2026 provider protocol before M1.
- **On-device quality** — needs the same measurement discipline as desktop: an eval set
  of real app prompts, scored on-device-vs-Claude, before trusting any offload.
- **Device coverage** — a large share of installed iPhones lack Apple Intelligence;
  the frontier-only fallback must be first-class, not an afterthought.
- **Key security** — pushes toward recommending a proxied endpoint, which nudges the
  roadmap toward the hosted gateway sooner than planned.
- **Positioning** — for apps (unlike a flat Claude *subscription*), the win is metered
  **$ cost**, not quota. The SDK's headline metric should flex to "spend conserved"
  in the app context while staying honest that it's an estimate.

---

## 12. What to reuse from the Go repo

| Reuse (port to Swift) | Leave behind |
|---|---|
| `keyword.go` verb ladder + length signal | The Ollama backend |
| The `Router`/`Backend` interface shape | The reverse-proxy / SSE-translation layer |
| `Outcome`/offload-guard concept | `classifyIntent` / wrapper stripping |
| `usage` summary model (tokens kept, offload rate, est. saved) | SQLite specifics |
| Measurement-first + default-to-safety discipline | The desktop auth/token model |
| Later: the `generalization.jsonl`-trained policy | — |
