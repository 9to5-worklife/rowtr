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
}

// Built-in defaults.
const (
	DefaultFrontierModel = "claude-opus-4-8"
	DefaultLocalModel    = "gemma3n:e2b"
	DefaultOllamaHost    = "http://localhost:11434"
	DefaultProxyAddr     = "127.0.0.1:8787"
)

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		FrontierModel: DefaultFrontierModel,
		LocalModel:    DefaultLocalModel,
		OllamaHost:    DefaultOllamaHost,
		ProxyAddr:     DefaultProxyAddr,
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
	return cfg
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

// EstimateCostUSD returns the estimated dollar cost of a completion. Unknown
// models cost $0 — local models run on the user's own hardware by design.
func EstimateCostUSD(model string, inputTokens, outputTokens int) float64 {
	p, ok := prices[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)/1e6*p.InputPerM + float64(outputTokens)/1e6*p.OutputPerM
}
