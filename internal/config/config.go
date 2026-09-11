// Package config loads ~/.config/dtc/config.toml. Real hostnames, hub URLs
// and tokens stay in these gitignored per-machine files — never in the repo.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type HostCfg struct {
	// SSH is the ssh destination used to reach the machine (an ~/.ssh/config
	// alias or user@host). Empty means "this machine, run tmux locally".
	SSH string `toml:"ssh"`
}

type Config struct {
	HubURL    string             `toml:"hub_url"`
	TokenFile string             `toml:"token_file"`
	Hostname  string             `toml:"hostname"`
	Hosts     map[string]HostCfg `toml:"hosts"`

	Dir   string // config dir (~/.config/dtc)
	Token string // resolved token (env > token file)
}

func Path() string {
	if p := os.Getenv("DTC_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dtc", "config.toml")
}

// Load reads the config file. A missing file is not an error — the tool then
// runs in degraded local-only mode and says so.
func Load() (*Config, error) {
	cfg := &Config{Hosts: map[string]HostCfg{}}
	path := Path()
	cfg.Dir = filepath.Dir(path)

	data, rerr := os.ReadFile(path)
	if errors.Is(rerr, os.ErrNotExist) {
		cfg.Hostname = defaultHostname()
		cfg.Token = os.Getenv("DTC_TOKEN")
		return cfg, nil
	}
	if rerr != nil {
		return nil, rerr
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.HubURL == "" {
		cfg.HubURL = os.Getenv("DTC_HUB_URL")
	}
	cfg.HubURL = strings.TrimRight(cfg.HubURL, "/")

	if t := os.Getenv("DTC_TOKEN"); t != "" {
		cfg.Token = t
	} else {
		tf := cfg.TokenFile
		if tf == "" {
			tf = "token"
		}
		if !filepath.IsAbs(tf) {
			tf = filepath.Join(cfg.Dir, tf)
		}
		if b, err := os.ReadFile(tf); err == nil {
			cfg.Token = strings.TrimSpace(string(b))
		}
	}

	if cfg.Hostname == "" {
		cfg.Hostname = defaultHostname()
	}
	return cfg, nil
}

func defaultHostname() string {
	if h := os.Getenv("DTC_HOSTNAME"); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// SSHFor returns the ssh destination for an agent-reported hostname, or
// ok=false when the fleet map has no entry (remote ops unavailable).
func (c *Config) SSHFor(host string) (string, bool) {
	hc, ok := c.Hosts[host]
	if !ok {
		return "", false
	}
	return hc.SSH, true
}

// IsLocal reports whether sessions reported by `host` are on this machine.
func (c *Config) IsLocal(host string) bool {
	return host == c.Hostname
}
