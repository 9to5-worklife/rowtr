// Command rowtr is the slice-1 CLI: it takes a prompt, routes it to the local or
// frontier model, and prints the response plus the routing metrics.
//
//	rowtr "define entropy"                      # auto-routed
//	rowtr --tier frontier "draft a memo"        # force a tier
//	rowtr --json "compare X and Y" > result.json
//	echo "summarize this" | rowtr               # prompt from stdin
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/9to5-worklife/rowtr/internal/backend"
	"github.com/9to5-worklife/rowtr/internal/config"
	"github.com/9to5-worklife/rowtr/internal/eval"
	"github.com/9to5-worklife/rowtr/internal/pipeline"
	"github.com/9to5-worklife/rowtr/internal/proxy"
	"github.com/9to5-worklife/rowtr/internal/router"
	"github.com/9to5-worklife/rowtr/internal/setup"
	"github.com/9to5-worklife/rowtr/internal/usage"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version"):
		fmt.Printf("rowtr %s\n", version)
		return
	case len(os.Args) > 1 && os.Args[1] == "setup":
		err = runSetup(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "serve":
		err = runServe(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "score":
		err = runScore(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "usage":
		err = runUsage(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "claude":
		err = runClaude(os.Args[2:])
	case len(os.Args) == 1 && doubleClicked():
		err = runWelcome()
	default:
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runUsage prints the usage summary — the cross-platform, GUI-free view of the
// menu-bar app's numbers.
func runUsage(_ []string) error {
	p, err := config.UsagePath()
	if err != nil {
		return err
	}
	st, err := usage.Open(p)
	if err != nil {
		return fmt.Errorf("opening usage store: %w", err)
	}
	defer st.Close()

	sum, err := st.Summary()
	if err != nil {
		return err
	}
	rate := 0.0
	if sum.Total > 0 {
		rate = 100 * float64(sum.Local) / float64(sum.Total)
	}

	fmt.Println("Rowtr usage")
	fmt.Printf("  Kept off Claude:  %d requests · %d tokens\n", sum.Local, sum.LocalTokens)
	fmt.Printf("  Spent on Claude:  %d requests · %d tokens processed\n", sum.Frontier, sum.FrontierProcessed())
	if sum.CacheReadTokens > 0 || sum.CacheWriteTokens > 0 {
		fmt.Printf("    prompt cache:   %.0f%% of input served from cache (%d read · %d written)\n",
			100*sum.CacheHitRate(), sum.CacheReadTokens, sum.CacheWriteTokens)
	}
	fmt.Printf("  Offload rate:     %.0f%% of requests (%d/%d)", rate, sum.Local, sum.Total)
	if allTok := sum.LocalTokens + sum.FrontierTokens; allTok > 0 && sum.FrontierTokens > 0 {
		fmt.Printf(" · %.0f%% of tokens", 100*float64(sum.LocalTokens)/float64(allTok))
	}
	fmt.Println()
	if len(sum.ByCategory) > 0 {
		fmt.Println("\n  Claude tokens by category:")
		total := 0
		for _, c := range sum.ByCategory {
			total += c.Tokens
		}
		for _, c := range sum.ByCategory {
			share := 0.0
			if total > 0 {
				share = 100 * float64(c.Tokens) / float64(total)
			}
			fmt.Printf("    %-10s %3.0f%%  (%d requests · %d tokens)\n", c.Category, share, c.Count, c.Tokens)
		}
	}
	if len(sum.ByModel) > 0 {
		fmt.Println("\n  By model:")
		for _, m := range sum.ByModel {
			fmt.Printf("    %-24s %-11s %d\n", m.Model, "("+m.Tier+")", m.Count)
		}
	}
	if sum.MaxPromptTokens > bloatedContextTokens {
		fmt.Printf("\n  Largest context seen: %d tokens — long sessions resend it every turn;\n"+
			"  /clear between tasks (or /compact) keeps turns cheap.\n", sum.MaxPromptTokens)
	}
	fmt.Printf("\n  Est. $ saved (API pricing only): $%.4f\n", sum.SavedUSD)
	return nil
}

// bloatedContextTokens is where a conversation's resent-every-turn context is
// worth flagging to the user.
const bloatedContextTokens = 150_000

// runClaude ensures a verified Rowtr proxy is running (starting one in the
// background if needed), then launches Claude Code pointed at it with the auth
// header injected — no ANTHROPIC_BASE_URL for the user to remember. The
// rowtr-owned --route/--observe flags are consumed here and persist the routing
// choice; everything else passes through to claude.
func runClaude(args []string) error {
	mode := ""
	rest := args[:0:0]
	for _, a := range args {
		switch a {
		case "--route":
			mode = "route"
		case "--observe":
			mode = "observe"
		default:
			rest = append(rest, a)
		}
	}
	args = rest

	cfg := config.Load()
	switch {
	case mode != "": // explicit flag wins and is remembered
		if mode != cfg.ProxyMode {
			persistProxyMode(mode)
		}
	case cfg.ProxyMode != "":
		mode = cfg.ProxyMode
	default:
		mode = askRoutingConsent(cfg)
	}

	base := "http://" + cfg.ProxyAddr
	tokenPath, err := config.TokenPath()
	if err != nil {
		return err
	}
	token := readToken(tokenPath)

	st := probeProxy(base, token)
	switch {
	case !st.responding:
		if portBusy(cfg.ProxyAddr) {
			return fmt.Errorf("port %s is in use by something that isn't a Rowtr proxy\n"+
				"  stop it, or pick another port: export ROWTR_PROXY_ADDR=127.0.0.1:8790", cfg.ProxyAddr)
		}
		if err := startProxyDetached(mode); err != nil {
			return fmt.Errorf("starting rowtr proxy: %w", err)
		}
		for i := 0; i < 20; i++ {
			token = readToken(tokenPath) // the proxy creates it on first start
			if st = probeProxy(base, token); st.isRowtr {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !st.isRowtr {
			return fmt.Errorf("proxy didn't come up on %s — check proxy.log in the rowtr config dir", cfg.ProxyAddr)
		}
		if token != "" && !st.verified {
			return fmt.Errorf("the listener on %s did not prove it holds %s — refusing to send traffic to it", cfg.ProxyAddr, tokenPath)
		}
		fmt.Fprintf(os.Stderr, "rowtr: proxy started on %s (%s mode; it keeps running after claude exits)\n", base, mode)
	case !st.isRowtr:
		return fmt.Errorf("port %s is in use by something that isn't a Rowtr proxy\n"+
			"  stop it, or pick another port: export ROWTR_PROXY_ADDR=127.0.0.1:8790", cfg.ProxyAddr)
	case token != "" && !st.verified:
		return fmt.Errorf("something on %s answers like a Rowtr proxy but can't prove it holds %s\n"+
			"  likely an old rowtr build (restart it: pkill rowtr && rowtr claude) or another user's process", cfg.ProxyAddr, tokenPath)
	default:
		if token == "" {
			fmt.Fprintf(os.Stderr, "rowtr: running proxy predates client auth — restart it when convenient: pkill rowtr && rowtr claude\n")
		}
		fmt.Fprintf(os.Stderr, "rowtr: using running proxy on %s\n", base)
		if st.mode != "" && st.mode != mode {
			fmt.Fprintf(os.Stderr, "rowtr: note — the running proxy is in %s mode, not %s; switch with: pkill rowtr && rowtr claude\n", st.mode, mode)
		}
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH — install Claude Code first")
	}
	env := append(os.Environ(), "ANTHROPIC_BASE_URL="+base)
	if token != "" {
		hdr := proxy.AuthHeader + ": " + token
		if existing := os.Getenv("ANTHROPIC_CUSTOM_HEADERS"); existing != "" {
			hdr = existing + "\n" + hdr
		}
		env = append(env, "ANTHROPIC_CUSTOM_HEADERS="+hdr)
	}
	cmd := exec.Command(claudePath, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	start := time.Now()
	runErr := cmd.Run()
	printSessionSummary(start, mode)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			os.Exit(exitErr.ExitCode()) // propagate claude's exit code
		}
		return runErr
	}
	return nil
}

// persistProxyMode saves the routing choice on top of the file config only —
// never the env-resolved one, so one-off overrides don't get baked in.
func persistProxyMode(mode string) {
	fc := config.LoadFile()
	fc.ProxyMode = mode
	if _, err := config.Save(fc); err != nil {
		fmt.Fprintf(os.Stderr, "rowtr: couldn't persist mode choice: %v\n", err)
	}
}

// askRoutingConsent is the first-run routing opt-in: local offload only starts
// after the user says yes, and the choice is persisted. Non-interactive runs
// default to observe (log-only) without persisting anything.
func askRoutingConsent(cfg config.Config) string {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "rowtr: no routing choice on record — using observe mode (log-only); opt in with `rowtr claude --route`")
		return "observe"
	}
	fmt.Fprintf(os.Stderr, "rowtr can answer simple, tool-free prompts with your local model (%s)\n"+
		"instead of Claude, keeping them off your Claude quota. Anything that needs\n"+
		"Claude — tools, reasoning, ambiguous asks — still goes to Claude.\n", cfg.LocalModel)
	fmt.Fprint(os.Stderr, "Route eligible prompts to the local model? [Y/n] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		// stdin looked like a terminal but produced no input (/dev/null, EOF):
		// silence is not consent — observe, and leave the question open.
		fmt.Fprintln(os.Stderr, "\nrowtr: no answer — using observe mode (log-only); opt in with `rowtr claude --route`")
		return "observe"
	}
	mode := "route"
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "", "y", "yes":
	default:
		mode = "observe"
		fmt.Fprintln(os.Stderr, "rowtr: observe mode — everything goes to Claude; routing is logged, not enforced (`rowtr claude --route` to change)")
	}
	persistProxyMode(mode)
	return mode
}

// printSessionSummary discloses what the proxy did with this session's traffic
// — the offload is invisible inside Claude Code, so it's stated where the user
// actually looks.
func printSessionSummary(since time.Time, mode string) {
	p, err := config.UsagePath()
	if err != nil {
		return
	}
	st, err := usage.Open(p)
	if err != nil {
		return
	}
	defer st.Close()
	sum, err := st.SummarySince(since)
	if err != nil {
		return
	}
	switch {
	case sum.Local > 0:
		model := ""
		for _, m := range sum.ByModel {
			if m.Tier == "local" {
				model = m.Model
				break
			}
		}
		fmt.Fprintf(os.Stderr, "rowtr: this session — %d request(s) · %d tokens answered locally by %s (kept off Claude)\n",
			sum.Local, sum.LocalTokens, model)
	case mode == "observe":
		fmt.Fprintln(os.Stderr, "rowtr: observe mode — everything went to Claude; opt into local routing with `rowtr claude --route`")
	default:
		fmt.Fprintln(os.Stderr, "rowtr: nothing was answered locally this session")
	}
	if sum.CacheReadTokens > 0 {
		fmt.Fprintf(os.Stderr, "rowtr: prompt cache served %.0f%% of this session's Claude input\n", 100*sum.CacheHitRate())
	}
	if sum.MaxPromptTokens > bloatedContextTokens {
		fmt.Fprintf(os.Stderr, "rowtr: tip — context reached %d tokens and is resent every turn; /clear between tasks keeps turns cheap\n",
			sum.MaxPromptTokens)
	}
}

type proxyStatus struct {
	responding bool
	isRowtr    bool
	verified   bool // listener proved it holds the local token file
	mode       string
}

// probeProxy challenges base's health endpoint with a nonce. `verified` means
// the listener answered with a valid HMAC over the nonce, i.e. it can read the
// same user-private token file we can — not merely that it mimics the JSON.
func probeProxy(base, token string) proxyStatus {
	var st proxyStatus
	nonce := randomNonce()
	req, err := http.NewRequest(http.MethodGet, base+"/rowtr/health?nonce="+nonce, nil)
	if err != nil {
		return st
	}
	if token != "" {
		req.Header.Set(proxy.AuthHeader, token)
	}
	resp, err := (&http.Client{Timeout: 1 * time.Second}).Do(req)
	if err != nil {
		return st
	}
	defer resp.Body.Close()
	st.responding = true
	var body struct {
		Service string `json:"service"`
		Proof   string `json:"proof"`
		Mode    string `json:"mode"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return st
	}
	st.isRowtr = body.Service == "rowtr"
	st.mode = body.Mode
	st.verified = token != "" && body.Proof == proxy.HealthProof(token, nonce)
	return st
}

func readToken(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func randomNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func portBusy(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// maxProxyLogBytes caps proxy.log: past this, the next start truncates it.
const maxProxyLogBytes = 5 << 20

// startProxyDetached re-execs this binary as `rowtr serve --mode <mode>`,
// detached, logging to a user-private proxy.log in the config dir.
func startProxyDetached(mode string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "serve", "--mode", mode)
	if dir, derr := config.DataDir(); derr == nil {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			logPath := filepath.Join(dir, "proxy.log")
			flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
			if info, serr := os.Stat(logPath); serr == nil && info.Size() > maxProxyLogBytes {
				flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			}
			if f, ferr := os.OpenFile(logPath, flags, 0o600); ferr == nil {
				_ = f.Chmod(0o600) // tighten a log created by an older build
				cmd.Stdout, cmd.Stderr = f, f
			}
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runSetup runs the first-run system check and writes a config file.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	yes := fs.Bool("yes", false, "accept recommendations without prompting (never installs or downloads by itself)")
	pull := fs.Bool("pull", false, "run `ollama pull` for the recommended model")
	install := fs.Bool("install", false, "install Ollama if it's missing")
	probe := fs.Bool("probe", false, "make a minimal Claude API call to verify credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup.Run(setup.Options{AssumeYes: *yes, Pull: *pull, Install: *install, Probe: *probe})
}

// runScore runs a router over a labeled eval set and prints the scoreboard.
// --router both is the head-to-head: same cases, keyword vs model, plus the
// list of cases where the two disagree.
func runScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	evals := fs.String("evals", "evals/router.jsonl", "path to the eval JSONL file")
	which := fs.String("router", "keyword", "router to score: keyword | model | both")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cases, err := eval.Load(*evals)
	if err != nil {
		return err
	}

	switch *which {
	case "keyword":
		fmt.Print(eval.Run(router.KeywordRouter{}, cases).String())
	case "model":
		fmt.Print(eval.Run(newModelRouter(config.Load()), cases).String())
	case "both":
		kw := eval.Run(router.KeywordRouter{}, cases)
		md := eval.Run(newModelRouter(config.Load()), cases)
		fmt.Printf("— keyword —\n%s\n— model —\n%s\n", kw.String(), md.String())
		printRouterDisagreements(kw, md)
	default:
		return fmt.Errorf("invalid --router %q: want keyword | model | both", *which)
	}
	return nil
}

// newModelRouter builds the experimental classifier-backed router from config.
// No Fallback: on the scoreboard and in shadow mode a broken classifier must
// score as broken, not silently borrow the keyword answer.
func newModelRouter(cfg config.Config) *router.ModelRouter {
	return &router.ModelRouter{Host: cfg.RouterHost(), Model: cfg.RouterModel}
}

// printRouterDisagreements lists the cases the two routers routed differently —
// exactly the cases the eval set needs more of.
func printRouterDisagreements(kw, md eval.Report) {
	var n int
	for i := range kw.Results {
		k, m := kw.Results[i], md.Results[i]
		if k.GotOffload == m.GotOffload {
			continue
		}
		if n == 0 {
			fmt.Println("— disagreements —")
		}
		n++
		fmt.Printf("  keyword=%-5v model=%-5v want=%-5v  %q\n      keyword: %s\n      model:   %s\n",
			k.GotOffload, m.GotOffload, k.Case.Offload, snippet(k.Case.Prompt, 80),
			k.Outcome.Reason, m.Outcome.Reason)
	}
	if n == 0 {
		fmt.Println("— no disagreements: both routers made identical offload calls —")
	}
}

func snippet(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// runServe starts the transparent Anthropic-compatible proxy.
//
//	observe (default): forward everything; log what WOULD be routed.
//	route:             also divert Local + tool-free requests to Ollama.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", config.Load().ProxyAddr, "listen address")
	upstream := fs.String("upstream", "https://api.anthropic.com", "frontier upstream base URL")
	mode := fs.String("mode", "observe", "observe (log only) | route (divert Local, tool-free requests to Ollama)")
	noAuth := fs.Bool("no-auth", false, "don't require the "+proxy.AuthHeader+" header on /v1/messages")
	unsafeRemote := fs.Bool("unsafe-remote", false, "allow a non-loopback listen address or a cleartext remote upstream")
	cascade := fs.Bool("cascade", false, "try-local-then-judge for tool-free prompts (route mode; experimental)")
	shadow := fs.Bool("shadow", false, "run the model router alongside the keyword router and log comparisons (experimental)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mode != string(proxy.ModeObserve) && *mode != string(proxy.ModeRoute) {
		return fmt.Errorf("invalid --mode %q: want observe | route", *mode)
	}
	if err := checkAddrSafety(*addr, *upstream, *unsafeRemote); err != nil {
		return err
	}

	cfg := config.Load()
	local := backend.NewOllama(cfg.OllamaHost, cfg.LocalModel)
	logger := log.New(os.Stderr, "rowtr ", log.LstdFlags)

	// Usage store (for the tray app). Optional — the proxy runs without it.
	var store *usage.Store
	if p, err := config.UsagePath(); err == nil {
		if st, oerr := usage.Open(p); oerr == nil {
			store = st
			defer store.Close()
			logger.Printf("usage store: %s", p)
		} else {
			logger.Printf("usage store disabled: %v", oerr)
		}
	}

	var token string
	if p, terr := config.TokenPath(); terr == nil {
		if token, terr = proxy.LoadOrCreateToken(p); terr != nil {
			logger.Printf("auth token unavailable (%v) — client auth disabled", terr)
		}
	}
	requireAuth := !*noAuth && token != ""

	downshift := cfg.DownshiftModel
	if downshift == "off" {
		downshift = ""
	}

	// Shadow experiment: the model router rides along and gets compared; the
	// keyword router stays authoritative. Disagreement lines carry prompt text
	// (they're the labeling corpus), so the log file is user-private.
	var shadowRouter router.Router
	var shadowLog io.Writer
	if *shadow || cfg.Shadow {
		shadowRouter = newModelRouter(cfg)
		if p, serr := config.ShadowPath(); serr == nil {
			f, ferr := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if ferr != nil {
				return fmt.Errorf("opening shadow log: %w", ferr)
			}
			defer f.Close()
			shadowLog = f
			logger.Printf("shadow router ON: %s @ %s — comparisons → %s", cfg.RouterModel, cfg.RouterHost(), p)
		}
	}

	srv, err := proxy.New(proxy.Options{
		Router: router.KeywordRouter{}, Upstream: *upstream, Local: local,
		Mode: proxy.Mode(*mode), Store: store, Log: logger,
		Token: token, RequireAuth: requireAuth,
		DebugIntent:    os.Getenv("ROWTR_DEBUG_INTENT") == "1",
		DownshiftModel: downshift,
		Cascade:        *cascade || cfg.Cascade,
		Shadow:         shadowRouter,
		ShadowLog:      shadowLog,
	})
	if err != nil {
		return err
	}

	logger.Printf("proxy listening on http://%s  (upstream %s, mode %s)", *addr, *upstream, *mode)
	if requireAuth {
		logger.Printf("client auth on — `rowtr claude` handles it; for a manual client:")
		logger.Printf(`  export ANTHROPIC_BASE_URL=http://%s ANTHROPIC_CUSTOM_HEADERS="%s: %s"`, *addr, proxy.AuthHeader, token)
	} else {
		logger.Printf("point a client at it:  export ANTHROPIC_BASE_URL=http://%s", *addr)
	}
	if *mode == string(proxy.ModeObserve) {
		logger.Printf("observe mode: every request forwarded unchanged; routing is logged, not enforced")
	} else {
		logger.Printf("route mode: Local + tool-free requests → Ollama (%s); everything else → frontier", cfg.LocalModel)
		if downshift != "" {
			logger.Printf("downshift: allowlisted housekeeping falls back to %s when the local model can't serve it", downshift)
		}
		if *cascade || cfg.Cascade {
			logger.Printf("cascade ON: tool-free frontier prompts get one judged local attempt first")
		}
	}
	return http.ListenAndServe(*addr, srv.Handler())
}

// checkAddrSafety refuses configurations that would expose the proxy — it
// speaks cleartext HTTP and forwards the user's Claude credentials — beyond
// the machine, unless the user explicitly opts in.
func checkAddrSafety(addr, upstream string, unsafeOK bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if !loopbackHost(host) {
		if !unsafeOK {
			return fmt.Errorf("refusing to listen on non-loopback %q — the proxy speaks cleartext HTTP and relays your Claude credentials\n"+
				"  bind 127.0.0.1, or pass --unsafe-remote if you accept that", addr)
		}
		fmt.Fprintf(os.Stderr, "WARNING: listening on %s — traffic to this proxy is unencrypted HTTP; anyone who can reach it can spend your Claude quota\n", addr)
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return fmt.Errorf("invalid upstream %q: %w", upstream, err)
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		if !unsafeOK {
			return fmt.Errorf("refusing cleartext upstream %q — credentials would cross the network unencrypted (use https://, or pass --unsafe-remote)", upstream)
		}
		fmt.Fprintf(os.Stderr, "WARNING: upstream %s is unencrypted HTTP to a remote host\n", upstream)
	}
	return nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func run() error {
	tierFlag := flag.String("tier", "auto", "routing tier: auto | local | frontier")
	verbose := flag.Bool("verbose", true, "print the routing decision and metrics")
	asJSON := flag.Bool("json", false, "emit the full result as JSON (implies -verbose=false stdout)")
	flag.Parse()

	prompt, err := readPrompt(flag.Args())
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("empty prompt: pass a prompt as an argument or on stdin")
	}

	forceTier, err := parseTier(*tierFlag)
	if err != nil {
		return err
	}

	cfg := config.Load()
	p := pipeline.New(
		router.KeywordRouter{},
		backend.NewOllama(cfg.OllamaHost, cfg.LocalModel),
		backend.NewAnthropic(cfg.FrontierModel),
	)

	result, err := p.Run(context.Background(), prompt, forceTier)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "→ %s (%s) via %s/%s · %dms · %d→%d tok · $%.5f\n",
			result.Decision.Tier, result.Decision.Reason,
			result.Backend, result.Model,
			result.LatencyMS, result.InTokens, result.OutTokens, result.EstCostUSD)
	}
	fmt.Println(result.Text)
	return nil
}

// readPrompt takes the prompt from CLI args (joined) or, failing that, piped
// stdin. With neither — an interactive terminal and no args — it prints usage
// instead of blocking on a read the user can't see coming.
func readPrompt(args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("no prompt given\n" +
			"  rowtr \"your prompt\"    route one prompt\n" +
			"  rowtr claude           start Claude Code through the Rowtr proxy\n" +
			"  rowtr setup            first-time setup\n" +
			"  rowtr usage            show what's been kept off your Claude quota")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return string(data), nil
}

// runWelcome greets a user who launched the binary from a file manager
// (double-click on Windows): explain what Rowtr is, offer setup, and hold the
// console open so it doesn't flash and vanish.
func runWelcome() error {
	fmt.Printf("Rowtr %s — an intelligent router for Claude Code\n\n", version)
	fmt.Println("Rowtr is a command-line tool. Day to day you'll run it from a terminal:")
	fmt.Println("  rowtr setup     first-time setup (checks Ollama, picks a local model)")
	fmt.Println("  rowtr claude    start Claude Code with Rowtr in front of it")
	fmt.Println("  rowtr usage     see what you've kept off your Claude quota")
	fmt.Println()

	in := bufio.NewReader(os.Stdin)
	fmt.Print("Run first-time setup now? [Y/n] ")
	line, _ := in.ReadString('\n')
	var err error
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "", "y", "yes":
		fmt.Println()
		err = setup.Run(setup.Options{})
	}
	if err != nil {
		fmt.Println("setup error:", err)
	}
	fmt.Print("\nPress Enter to close this window… ")
	_, _ = in.ReadString('\n')
	return err
}

// parseTier maps the --tier flag to an optional forced tier (nil = auto/route).
func parseTier(s string) (*router.Tier, error) {
	switch strings.ToLower(s) {
	case "auto", "":
		return nil, nil
	case "local":
		t := router.Local
		return &t, nil
	case "frontier":
		t := router.Frontier
		return &t, nil
	default:
		return nil, fmt.Errorf("invalid --tier %q: want auto | local | frontier", s)
	}
}
