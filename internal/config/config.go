// Package config holds Rowtr's tunable settings and price table. Resolution
// order is defaults → config file → environment variable, so `rowtr setup` can
// persist choices while env vars win for one-off overrides.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the resolved runtime configuration.
type Config struct {
	FrontierModel string `json:"frontier_model"`
	LocalModel    string `json:"local_model"`
	OllamaHost    string `json:"ollama_host"`
	ProxyAddr     string `json:"proxy_addr"`
	// ProxyMode is the user's routing consent for `rowtr claude`:
	// "" (not yet asked) | "observe" | "route".
	ProxyMode string `json:"proxy_mode,omitempty"`
	// DownshiftModel is the cheap Claude model that serves internal-safe
	// requests when the local model can't. "off" disables downshifting.
	DownshiftModel string `json:"downshift_model,omitempty"`
	// Cascade enables try-local-then-judge for tool-free prompts (route mode).
	Cascade bool `json:"cascade,omitempty"`
	// CascadeJudge picks who grades the local answer in the cascade:
	// "" / "local" = a second local pass (free, but the model grades itself);
	// "haiku" = the cheap Claude tier; any other value = an explicit judge model.
	CascadeJudge string `json:"cascade_judge,omitempty"`
	// Cache1h upgrades forwarded requests' ephemeral cache breakpoints from the
	// 5-minute default to Anthropic's 1-hour TTL, so a resent context survives
	// think-time gaps instead of cache-missing into a full-price re-read.
	Cache1h bool `json:"cache_1h,omitempty"`
	// RouterModel is the small classifier model the experimental model router
	// asks for tier decisions (shadow mode / `rowtr score --router model`).
	RouterModel string `json:"router_model,omitempty"`
	// RouterOllamaHost is where that classifier lives (e.g. a Raspberry Pi:
	// http://raspberrypi.local:11434). Empty falls back to OllamaHost.
	RouterOllamaHost string `json:"router_ollama_host,omitempty"`
	// Shadow runs the model router alongside the keyword router in the proxy,
	// logging comparisons without affecting traffic.
	Shadow bool `json:"shadow_router,omitempty"`
}

// Built-in defaults.
const (
	DefaultFrontierModel  = "claude-opus-4-8"
	DefaultLocalModel     = "gemma3n:e2b"
	DefaultOllamaHost     = "http://localhost:11434"
	DefaultProxyAddr      = "127.0.0.1:8787"
	DefaultDownshiftModel = "claude-haiku-4-5"
	DefaultRouterModel    = "gemma3:270m"
)

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		FrontierModel:  DefaultFrontierModel,
		LocalModel:     DefaultLocalModel,
		OllamaHost:     DefaultOllamaHost,
		ProxyAddr:      DefaultProxyAddr,
		DownshiftModel: DefaultDownshiftModel,
		RouterModel:    DefaultRouterModel,
	}
}

// DataDir is Rowtr's per-user directory (e.g. ~/.config/rowtr).
func DataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rowtr"), nil
}

// Path is the config file location.
func Path() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// UsagePath is the usage database location (written by the proxy, read by the tray).
func UsagePath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "usage.db"), nil
}

// ShadowPath is the shadow-router comparison log location. User-private:
// disagreement lines carry prompt text (the labeling corpus).
func ShadowPath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "shadow.jsonl"), nil
}

// CascadeOutcomePath is the cascade feedback log location. User-private: each
// line carries the prompt intent (the labeled "was local good enough?" corpus).
func CascadeOutcomePath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "cascade_outcomes.jsonl"), nil
}

// RouterHost resolves where the classifier model lives: the dedicated
// RouterOllamaHost if set (e.g. a Raspberry Pi), else the local-tier Ollama.
func (c Config) RouterHost() string {
	if c.RouterOllamaHost != "" {
		return c.RouterOllamaHost
	}
	return c.OllamaHost
}

// TokenPath is the proxy auth token location (user-private file, 0o600).
func TokenPath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "proxy-token"), nil
}

// Load resolves configuration: defaults → config file → environment variables.
func Load() Config {
	cfg := Defaults()
	if p, err := Path(); err == nil {
		if fc, err := loadFile(p); err == nil {
			merge(&cfg, fc)
		}
	}
	cfg.FrontierModel = env("ROWTR_FRONTIER_MODEL", cfg.FrontierModel)
	cfg.LocalModel = env("ROWTR_LOCAL_MODEL", cfg.LocalModel)
	cfg.OllamaHost = env("OLLAMA_HOST", cfg.OllamaHost)
	cfg.ProxyAddr = env("ROWTR_PROXY_ADDR", cfg.ProxyAddr)
	cfg.ProxyMode = env("ROWTR_PROXY_MODE", cfg.ProxyMode)
	cfg.DownshiftModel = env("ROWTR_DOWNSHIFT_MODEL", cfg.DownshiftModel)
	if os.Getenv("ROWTR_CASCADE") == "1" {
		cfg.Cascade = true
	}
	cfg.CascadeJudge = env("ROWTR_CASCADE_JUDGE", cfg.CascadeJudge)
	if os.Getenv("ROWTR_CACHE_1H") == "1" {
		cfg.Cache1h = true
	}
	cfg.RouterModel = env("ROWTR_ROUTER_MODEL", cfg.RouterModel)
	cfg.RouterOllamaHost = env("ROWTR_ROUTER_OLLAMA_HOST", cfg.RouterOllamaHost)
	if os.Getenv("ROWTR_SHADOW") == "1" {
		cfg.Shadow = true
	}
	return cfg
}

// LoadFile returns only the persisted config, without the env overlay — the
// right base when mutating one field and re-saving, so one-off environment
// overrides never get baked into the file.
func LoadFile() Config {
	var c Config
	if p, err := Path(); err == nil {
		if fc, err := loadFile(p); err == nil {
			c = fc
		}
	}
	return c
}

// Save writes cfg to the config file, creating the directory. Returns the path.
func Save(cfg Config) (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func loadFile(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

// merge overlays non-empty fields of src onto dst.
func merge(dst *Config, src Config) {
	if src.FrontierModel != "" {
		dst.FrontierModel = src.FrontierModel
	}
	if src.LocalModel != "" {
		dst.LocalModel = src.LocalModel
	}
	if src.OllamaHost != "" {
		dst.OllamaHost = src.OllamaHost
	}
	if src.ProxyAddr != "" {
		dst.ProxyAddr = src.ProxyAddr
	}
	if src.ProxyMode != "" {
		dst.ProxyMode = src.ProxyMode
	}
	if src.DownshiftModel != "" {
		dst.DownshiftModel = src.DownshiftModel
	}
	if src.Cascade {
		dst.Cascade = true
	}
	if src.CascadeJudge != "" {
		dst.CascadeJudge = src.CascadeJudge
	}
	if src.Cache1h {
		dst.Cache1h = true
	}
	if src.RouterModel != "" {
		dst.RouterModel = src.RouterModel
	}
	if src.RouterOllamaHost != "" {
		dst.RouterOllamaHost = src.RouterOllamaHost
	}
	if src.Shadow {
		dst.Shadow = true
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Price is a per-million-token price pair in USD.
type Price struct {
	InputPerM  float64
	OutputPerM float64
}

var prices = map[string]Price{
	"claude-opus-4-8":  {InputPerM: 5.00, OutputPerM: 25.00},
	"claude-sonnet-5":  {InputPerM: 3.00, OutputPerM: 15.00},
	"claude-haiku-4-5": {InputPerM: 1.00, OutputPerM: 5.00},
}

// Prompt-cache pricing multipliers on the base input rate (Anthropic): cache
// reads are 0.1×, 5-minute writes 1.25×, and 1-hour writes 2×.
const (
	CacheReadMult    = 0.1
	CacheWriteMult   = 1.25
	CacheWriteMult1h = 2.0
)

// EstimateCostUSD returns the estimated dollar cost of a completion. Unknown
// models cost $0 — local models run on the user's own hardware by design.
func EstimateCostUSD(model string, inputTokens, outputTokens int) float64 {
	return EstimateCostUSDCached(model, inputTokens, 0, 0, outputTokens)
}

// EstimateCostUSDCached prices a completion with cache-aware input rates,
// treating all cache writes as 5-minute (1.25×).
func EstimateCostUSDCached(model string, inputTokens, cacheReadTokens, cacheWriteTokens, outputTokens int) float64 {
	return EstimateCostUSDCached1h(model, inputTokens, cacheReadTokens, cacheWriteTokens, 0, outputTokens)
}

// EstimateCostUSDCached1h prices a completion splitting cache writes into
// 5-minute (1.25×) and 1-hour (2×) portions — the 1-hour TTL costs more to
// write, so pricing it correctly keeps the savings numbers honest.
func EstimateCostUSDCached1h(model string, inputTokens, cacheReadTokens, cacheWrite5mTokens, cacheWrite1hTokens, outputTokens int) float64 {
	p, ok := prices[model]
	if !ok {
		return 0
	}
	in := float64(inputTokens) +
		float64(cacheReadTokens)*CacheReadMult +
		float64(cacheWrite5mTokens)*CacheWriteMult +
		float64(cacheWrite1hTokens)*CacheWriteMult1h
	return in/1e6*p.InputPerM + float64(outputTokens)/1e6*p.OutputPerM
}
