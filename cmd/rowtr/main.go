// Command rowtr is the slice-1 CLI: it takes a prompt, routes it to the local or
// frontier model, and prints the response plus the routing metrics.
//
//	rowtr "define entropy"                      # auto-routed
//	rowtr --tier frontier "draft a memo"        # force a tier
//	rowtr --json "compare X and Y" > result.json
//	echo "summarize this" | rowtr               # prompt from stdin
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorhoulihan/rowtr/internal/backend"
	"github.com/connorhoulihan/rowtr/internal/config"
	"github.com/connorhoulihan/rowtr/internal/eval"
	"github.com/connorhoulihan/rowtr/internal/pipeline"
	"github.com/connorhoulihan/rowtr/internal/proxy"
	"github.com/connorhoulihan/rowtr/internal/router"
	"github.com/connorhoulihan/rowtr/internal/setup"
	"github.com/connorhoulihan/rowtr/internal/usage"
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
	fmt.Printf("  Spent on Claude:  %d requests · %d tokens\n", sum.Frontier, sum.FrontierTokens)
	fmt.Printf("  Offload rate:     %.0f%% of requests (%d/%d)", rate, sum.Local, sum.Total)
	if allTok := sum.LocalTokens + sum.FrontierTokens; allTok > 0 && sum.FrontierTokens > 0 {
		fmt.Printf(" · %.0f%% of tokens", 100*float64(sum.LocalTokens)/float64(allTok))
	}
	fmt.Println()
	if len(sum.ByModel) > 0 {
		fmt.Println("\n  By model:")
		for _, m := range sum.ByModel {
			fmt.Printf("    %-24s %-11s %d\n", m.Model, "("+m.Tier+")", m.Count)
		}
	}
	fmt.Printf("\n  Est. $ saved (API pricing only): $%.4f\n", sum.SavedUSD)
	return nil
}

// runClaude ensures a Rowtr proxy is running (starting one in the background if
// needed), then launches Claude Code pointed at it — no ANTHROPIC_BASE_URL for
// the user to remember. Extra args pass through to claude.
func runClaude(args []string) error {
	cfg := config.Load()
	base := "http://" + cfg.ProxyAddr

	if !proxyHealthy(base) {
		if portBusy(cfg.ProxyAddr) {
			return fmt.Errorf("port %s is in use by something that isn't a Rowtr proxy (an old rowtr build, or another service)\n"+
				"  stop it, or pick another port: export ROWTR_PROXY_ADDR=127.0.0.1:8790", cfg.ProxyAddr)
		}
		if err := startProxyDetached(); err != nil {
			return fmt.Errorf("starting rowtr proxy: %w", err)
		}
		up := false
		for i := 0; i < 20; i++ {
			if proxyHealthy(base) {
				up = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !up {
			return fmt.Errorf("proxy didn't come up on %s — check the log in the rowtr config dir", cfg.ProxyAddr)
		}
		fmt.Fprintf(os.Stderr, "rowtr: proxy started on %s (route mode; it keeps running after claude exits)\n", base)
	} else {
		fmt.Fprintf(os.Stderr, "rowtr: using running proxy on %s\n", base)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH — install Claude Code first")
	}
	cmd := exec.Command(claudePath, args...)
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+base)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode()) // propagate claude's exit code
		}
		return err
	}
	return nil
}

// proxyHealthy reports whether base answers as a Rowtr proxy (not just any server).
func proxyHealthy(base string) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(base + "/rowtr/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Service string `json:"service"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return false
	}
	return body.Service == "rowtr"
}

func portBusy(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// startProxyDetached re-execs this binary as `rowtr serve --mode route`,
// detached, logging to proxy.log in the config dir.
func startProxyDetached() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "serve", "--mode", "route")
	if dir, derr := config.DataDir(); derr == nil {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			if f, ferr := os.OpenFile(filepath.Join(dir, "proxy.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
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
	yes := fs.Bool("yes", false, "accept recommendations without prompting")
	pull := fs.Bool("pull", false, "run `ollama pull` for the recommended model")
	probe := fs.Bool("probe", false, "make a minimal Claude API call to verify credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup.Run(setup.Options{AssumeYes: *yes, Pull: *pull, Probe: *probe})
}

// runScore runs the router over a labeled eval set and prints the scoreboard.
func runScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	evals := fs.String("evals", "evals/router.jsonl", "path to the eval JSONL file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cases, err := eval.Load(*evals)
	if err != nil {
		return err
	}
	fmt.Print(eval.Run(router.KeywordRouter{}, cases).String())
	return nil
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mode != string(proxy.ModeObserve) && *mode != string(proxy.ModeRoute) {
		return fmt.Errorf("invalid --mode %q: want observe | route", *mode)
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

	srv, err := proxy.New(router.KeywordRouter{}, *upstream, local, proxy.Mode(*mode), store, logger)
	if err != nil {
		return err
	}

	logger.Printf("proxy listening on http://%s  (upstream %s, mode %s)", *addr, *upstream, *mode)
	logger.Printf("point a client at it:  export ANTHROPIC_BASE_URL=http://%s", *addr)
	if *mode == string(proxy.ModeObserve) {
		logger.Printf("observe mode: every request forwarded unchanged; routing is logged, not enforced")
	} else {
		logger.Printf("route mode: Local + tool-free requests → Ollama (%s); everything else → frontier", cfg.LocalModel)
	}
	return http.ListenAndServe(*addr, srv.Handler())
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

// readPrompt takes the prompt from CLI args (joined) or, failing that, stdin.
func readPrompt(args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return string(data), nil
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
