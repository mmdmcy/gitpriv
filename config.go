package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the on-disk configuration (~/.config/gitpriv/config.json, or
// $GITPRIV_CONFIG).
//
// Include and Skip are *added* to the built-in defaults; they never replace
// them. A file is synced only when it is git-ignored, matches an include
// pattern, matches no skip pattern and is not larger than MaxFileBytes.
type Config struct {
	Store        string   `json:"store"`
	Include      []string `json:"include,omitempty"`
	Skip         []string `json:"skip,omitempty"`
	MaxFileBytes int64    `json:"max_file_bytes,omitempty"`
}

// defaultInclude is the allowlist of files that look like private state.
var defaultInclude = []string{
	"**/.env*",
	"**/*.pem",
	"**/*.key",
	"**/*.p12",
	"**/*.p8",
	"**/*.jks",
	"**/*.keystore",
	"**/*.private.*",
	"**/secrets/**",
	"**/.secrets/**",
	"**/pb_data/**",
	"**/*.sqlite",
	"**/*.sqlite3",
	"**/*.db",
}

// defaultSkip is junk that is never synced, even when it matches an include.
var defaultSkip = []string{
	"**/.git/**",
	"**/node_modules/**",
	"**/.venv*/**",
	"**/venv/**",
	"**/__pycache__/**",
	"**/.pytest_cache/**",
	"**/.mypy_cache/**",
	"**/.ruff_cache/**",
	"**/.next/**",
	"**/out/**",
	"**/dist/**",
	"**/build/**",
	"**/target/**",
	"**/.turbo/**",
	"**/.cache/**",
	"**/coverage/**",
	"**/Pods/**",
	"**/android/.gradle/**",
	"**/*.tsbuildinfo",
	"**/.DS_Store",
	"**/*.log",
}

const defaultMaxFileBytes = 5 << 20 // 5 MiB

// Rules is the effective filter: built-in defaults merged with the config.
type Rules struct {
	Include      []string
	Skip         []string
	MaxFileBytes int64
}

func (c *Config) Rules() Rules {
	r := Rules{
		Include:      append(append([]string{}, defaultInclude...), c.Include...),
		Skip:         append(append([]string{}, defaultSkip...), c.Skip...),
		MaxFileBytes: c.MaxFileBytes,
	}
	if r.MaxFileBytes <= 0 {
		r.MaxFileBytes = defaultMaxFileBytes
	}
	return r
}

func configPath() string {
	if p := os.Getenv("GITPRIV_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "gitpriv.json"
	}
	return filepath.Join(home, ".config", "gitpriv", "config.json")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// loadConfig reads the config. When needStore is false a missing config is
// fine and the defaults are used, so `gitpriv scan` works before `init`.
func loadConfig(needStore bool) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(configPath())
	if err != nil {
		if !needStore && errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("no config at %s (run: gitpriv init --store <path>)", configPath())
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("bad config %s: %w", configPath(), err)
	}
	cfg.Store = expandHome(cfg.Store)
	if needStore {
		if cfg.Store == "" {
			return nil, fmt.Errorf("config %s has no store", configPath())
		}
		if err := checkStore(cfg.Store); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// checkStore verifies the store is a git work tree.
func checkStore(store string) error {
	out, err := gitOutput(store, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		return fmt.Errorf("store %s is not a git work tree (git init or clone it first)", store)
	}
	return nil
}

func cmdInit(args []string) error {
	var store string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--store":
			if i+1 >= len(args) {
				return errors.New("--store needs a path")
			}
			i++
			store = args[i]
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if store == "" {
		return errors.New("usage: gitpriv init --store <path>")
	}
	abs, err := filepath.Abs(expandHome(store))
	if err != nil {
		return err
	}
	if err := checkStore(abs); err != nil {
		return err
	}
	// Keep include/skip/max_file_bytes the user already set; only the store
	// changes. Built-in defaults are never written into the file, so they
	// can improve with new versions.
	cfg := &Config{}
	if data, err := os.ReadFile(configPath()); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("existing config %s is invalid: %w", configPath(), err)
		}
	}
	cfg.Store = abs
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath(), append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (store: %s)\n", configPath(), abs)
	return nil
}
