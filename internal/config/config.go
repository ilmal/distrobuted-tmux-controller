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
	// Hidden marks a client machine (e.g. a laptop): its sessions are left out
	// of fleet views. The local machine is never hidden from itself, and
	// dashboards can reveal hidden hosts on demand (TUI `H`, `ls --all`).
	Hidden bool `toml:"hidden"`
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

// HiddenHost reports whether a fleet host should stay out of fleet views.
// `selfDeclared` is the host's own `hidden` flag as reported by the hub (it
// marks itself a client machine), which lets every dashboard agree without
// repeating the setting in each machine's config.
//
// This applies to the machine you are running on too: a client machine's own
// sessions are scratch terminal windows, and showing them on the client itself
// is exactly the noise the flag exists to remove. Reveal is always available
// (TUI `H`, `ls --all`, hub `?all=1`).
func (c *Config) HiddenHost(host string, selfDeclared bool) bool {
	return selfDeclared || c.Hosts[host].Hidden
}

// IsHidden is HiddenHost using only the local config (no heartbeat flag).
func (c *Config) IsHidden(host string) bool { return c.HiddenHost(host, false) }

// SelfHidden reports whether this machine declares itself a client machine
// (its own [hosts.<self>] table carries hidden = true). The agent sends this
// in every heartbeat so the hub — and therefore every dashboard, including
// the hub's web page — can leave it out of fleet views without each machine
// repeating the setting.
func (c *Config) SelfHidden() bool {
	return c.Hosts[c.Hostname].Hidden
}
