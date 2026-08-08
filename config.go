package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Store string   `json:"store"`
	Skip  []string `json:"skip,omitempty"`
}

var defaultSkip = []string{
	"**/node_modules/**",
	"**/build/**",
	"**/dist/**",
	"**/target/**",
	"**/vendor/**",
	"**/__pycache__/**",
	"**/.cache/**",
	"**/.venv/**",
	"**/venv/**",
	"**/logs/**",
	"**/tmp/**",
	"**/*.log",
	"**/*.tmp",
	"**/*.swp",
	"**/*~",
	"**/core",
	"**/core.*",
	".DS_Store",
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "gitpriv.json"
	}
	return filepath.Join(home, ".config", "gitpriv", "config.json")
}

func loadConfig() (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(configPath())
	if err != nil {
		return nil, fmt.Errorf("no config at %s (run: gitpriv init --store <path>)", configPath())
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("bad config %s: %w", configPath(), err)
	}
	if cfg.Store == "" {
		return nil, errors.New("config has no store")
	}
	if len(cfg.Skip) == 0 {
		cfg.Skip = defaultSkip
	}
	return cfg, nil
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
		return errors.New("gitpriv init --store <path>")
	}
	abs, err := filepath.Abs(store)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("store %s: %w", abs, err)
	}
	cfg := &Config{Store: abs, Skip: defaultSkip}
	dir := filepath.Dir(configPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(data, '\n'), 0o600)
}
