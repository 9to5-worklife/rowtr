// Package setup implements `rowtr setup`: a first-run check that recommends a
// local model for the host, verifies Ollama and Claude access, and writes a
// config file so Rowtr is genuinely "download and run".
package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/connorhoulihan/rowtr/internal/config"
	"github.com/connorhoulihan/rowtr/internal/sysinfo"
)

// Options controls the setup run.
type Options struct {
	AssumeYes bool // accept prompts non-interactively
	Pull      bool // run `ollama pull` for the recommended model
	Probe     bool // make a minimal Claude API call to verify credentials
}

// Run executes the interactive setup.
func Run(opts Options) error {
	in := bufio.NewReader(os.Stdin)
	line := strings.Repeat("─", 44)
	fmt.Println("Rowtr setup")
	fmt.Println(line)

	cfg := config.Load()

	info := sysinfo.Detect()
	fmt.Printf("System:  %s/%s", info.OS, info.Arch)
	if info.TotalRAMGB > 0 {
		fmt.Printf(", %.0f GB RAM", info.TotalRAMGB)
	}
	if info.AppleSilicon {
		fmt.Print(" (Apple Silicon)")
	}
	fmt.Println()

	model, why := RecommendModel(info)
	fmt.Printf("Local model:  recommend %s\n              %s\n", model, why)

	reachable, models, detail := CheckOllama(cfg.OllamaHost)
	fmt.Printf("Ollama:  %s\n", detail)
	if !reachable {
		reachable, models = bootstrapOllama(in, opts, cfg.OllamaHost)
	}

	pulled := contains(models, model)
	if reachable && !pulled {
		// A multi-GB download must be an explicit choice: --pull or an
		// interactive yes. --yes alone deliberately does NOT trigger it.
		wantPull := opts.Pull
		if !wantPull && !opts.AssumeYes {
			wantPull = confirm(in, opts, fmt.Sprintf("Pull %s now? (downloads the model)", model), false)
		}
		if wantPull {
			if err := pullModel(model); err != nil {
				fmt.Printf("  pull failed: %v\n  run it yourself: ollama pull %s\n", err, model)
			} else {
				pulled = true
			}
		} else {
			fmt.Printf("  when ready:  ollama pull %s\n", model)
		}
	}

	// Configure the recommendation if present, else fall back to whatever is
	// already installed so the user isn't blocked.
	chosen := model
	if !pulled && len(models) > 0 {
		chosen = models[0]
		fmt.Printf("  using already-installed %s for now\n", chosen)
	}

	cstatus, _ := CheckClaude(opts, cfg.FrontierModel)
	fmt.Printf("Claude:  %s\n", cstatus)

	cfg.LocalModel = chosen
	if opts.AssumeYes || confirm(in, opts, fmt.Sprintf("Save config (local model = %s)?", chosen), true) {
		p, err := config.Save(cfg)
		if err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Printf("Saved config → %s\n", p)
	}

	fmt.Println(line)
	fmt.Println("Next — one command starts the proxy and Claude Code together:")
	fmt.Println("  rowtr claude")
	return nil
}

// RecommendModel picks a local model (an Ollama tag) sized to the host's RAM.
func RecommendModel(info sysinfo.Info) (model, why string) {
	ram := info.TotalRAMGB
	switch {
	case ram >= 32:
		model, why = "llama3.1:8b", fmt.Sprintf("%.0f GB RAM comfortably runs an ~8B model", ram)
	case ram >= 16:
		model, why = "gemma3n:e4b", fmt.Sprintf("%.0f GB RAM suits a ~4B model", ram)
	case ram >= 8:
		model, why = "gemma3n:e2b", fmt.Sprintf("%.0f GB RAM suits a small ~2B model", ram)
	case ram > 0:
		return "llama3.2:1b", fmt.Sprintf("%.0f GB RAM — use a 1B model", ram)
	default:
		return "gemma3n:e2b", "couldn't read RAM — defaulting to a small model"
	}
	if !info.AppleSilicon {
		why += "; no Apple-Silicon GPU, so inference is CPU-bound (slower) — a smaller model may feel snappier"
	}
	return model, why
}

// CheckOllama probes the daemon and lists installed models.
func CheckOllama(host string) (reachable bool, models []string, detail string) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(host + "/api/tags")
	if err != nil {
		return false, nil, fmt.Sprintf("not reachable at %s — install & start it: `brew install ollama && ollama serve`", host)
	}
	defer resp.Body.Close()

	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	for _, m := range out.Models {
		models = append(models, m.Name)
	}
	if len(models) == 0 {
		return true, nil, fmt.Sprintf("running at %s, but no models pulled yet", host)
	}
	return true, models, fmt.Sprintf("running at %s — installed: %s", host, strings.Join(models, ", "))
}

// CheckClaude reports whether Claude access is available. It verifies credentials
// exist (with --probe, that they work) but cannot distinguish a subscription
// from an API key.
func CheckClaude(opts Options, model string) (status string, ok bool) {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if opts.Probe {
			if err := probeClaude(key, model); err != nil {
				return fmt.Sprintf("ANTHROPIC_API_KEY set, but a test call failed: %v", err), false
			}
			return "ANTHROPIC_API_KEY set and a test call succeeded", true
		}
		return "ANTHROPIC_API_KEY is set (add --probe to test it)", true
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return "no ANTHROPIC_API_KEY, but Claude Code is installed — the proxy forwards its login (subscription or key)", true
	}
	return "no Claude credentials found — set ANTHROPIC_API_KEY, or log in with Claude Code (`claude`)", false
}

func probeClaude(key, model string) error {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// bootstrapOllama takes a machine with no reachable Ollama to a running daemon:
// installs (with consent) if missing, then starts it. Returns the post-bootstrap state.
func bootstrapOllama(in *bufio.Reader, opts Options, host string) (bool, []string) {
	if !ollamaInstalled() {
		if !confirm(in, opts, "Ollama isn't installed. Install it now?", true) {
			fmt.Println("  skipped — install later from https://ollama.com/download")
			return false, nil
		}
		if err := installOllama(); err != nil {
			fmt.Printf("  install failed: %v\n  install manually: https://ollama.com/download\n", err)
			return false, nil
		}
	}

	fmt.Print("  starting the Ollama daemon… ")
	if err := startOllama(); err != nil {
		fmt.Printf("failed: %v\n  start it yourself: `ollama serve`\n", err)
		return false, nil
	}
	for i := 0; i < 30; i++ { // up to ~15s for the daemon to come up
		if ok, models, _ := CheckOllama(host); ok {
			fmt.Println("running")
			return true, models
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Println("timed out — start it yourself: `ollama serve`")
	return false, nil
}

func ollamaInstalled() bool {
	if _, err := exec.LookPath("ollama"); err == nil {
		return true
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/Applications/Ollama.app"); err == nil {
			return true
		}
	}
	return false
}

// installOllama uses the platform's officially documented install method; where
// no package manager exists it points at the download page instead of improvising.
func installOllama() error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return runVisible("brew", "install", "ollama")
		}
		return fmt.Errorf("Homebrew not found — download the app from https://ollama.com/download")
	case "linux":
		return runVisible("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
	case "windows":
		if _, err := exec.LookPath("winget"); err == nil {
			return runVisible("winget", "install", "-e", "--id", "Ollama.Ollama")
		}
		return fmt.Errorf("winget not found — download the installer from https://ollama.com/download")
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

// startOllama launches the daemon detached so it outlives setup. On macOS the
// menu-bar app is preferred when present; otherwise `ollama serve`.
func startOllama() error {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/Applications/Ollama.app"); err == nil {
			return exec.Command("open", "-a", "Ollama").Run()
		}
	}
	path, err := exec.LookPath("ollama")
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "serve")
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runVisible(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

func confirm(in *bufio.Reader, opts Options, prompt string, def bool) bool {
	if opts.AssumeYes {
		return true
	}
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s] ", prompt, hint)
	line, err := in.ReadString('\n')
	if err != nil {
		return def
	}
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

func pullModel(model string) error {
	if _, err := exec.LookPath("ollama"); err != nil {
		return fmt.Errorf("ollama not in PATH")
	}
	cmd := exec.Command("ollama", "pull", model)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
