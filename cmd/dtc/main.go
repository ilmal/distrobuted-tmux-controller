// dtc — distributed tmux controller.
//
// One binary, three roles:
//
//	dtc          interactive fleet dashboard (TUI)
//	dtc ls       non-interactive fleet table
//	dtc attach   attach (or create) a session on any host
//	dtc agent    heartbeat local tmux state to the hub
//	dtc hub      the central registry server
//	dtc init     write a config template
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/agent"
	"github.com/ilmal/distrobuted-tmux-controller/internal/client"
	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/hub"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
	"github.com/ilmal/distrobuted-tmux-controller/internal/tmux"
	"github.com/ilmal/distrobuted-tmux-controller/internal/tui"
)

const version = "0.1.0"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dtc:", err)
	os.Exit(1)
}

func loadConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	return cfg
}

func main() {
	if len(os.Args) < 2 {
		cfg := loadConfig()
		if err := tui.Run(cfg); err != nil {
			fatal(err)
		}
		return
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "tui":
		err = tui.Run(loadConfig())
	case "ls":
		err = cmdLs(args)
	case "attach":
		err = cmdAttach(args)
	case "new":
		err = cmdNew(args)
	case "kill":
		err = cmdKill(args)
	case "color":
		err = cmdColor(args)
	case "agent":
		err = cmdAgent(args)
	case "hub":
		err = cmdHub(args)
	case "init":
		err = cmdInit()
	case "version", "--version":
		fmt.Println("dtc " + version)
	case "help", "--help", "-h":
		usage()
	default:
		// `dtc <name>` is sugar for attach
		err = cmdAttach([]string{cmd})
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Print(`dtc — distributed tmux controller

  dtc                 interactive dashboard (TUI)
  dtc ls              fleet table (non-interactive)
  dtc attach NAME     attach-or-create NAME on its host
  dtc new NAME        create NAME on a host (--host, default local)
  dtc kill NAME       kill session
  dtc color NAME C    set session color (see palette below)
  dtc agent [--once]  heartbeat to the hub (daemon mode: systemd/cron)
  dtc hub             run the registry server
  dtc init            write a config template

config: ~/.config/dtc/config.toml (see config.example.toml in the repo)
colors: ` + colorNames() + `
`)
}

func colorNames() string {
	var names []string
	for _, d := range colors.Palette {
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

// ---- ls ----

func cmdLs(args []string) error {
	cfg := loadConfig()
	sortMode := "color"
	all := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--sort", "-s":
			i++
			if i < len(args) {
				sortMode = args[i]
			}
		case "--all":
			all = true
		}
	}
	fr, err := client.FetchFleet(cfg)
	if err != nil {
		return err
	}
	type r struct {
		model.Session
		def colors.Def
	}
	var rows []r
	hidden := 0
	shownHosts := 0
	for _, h := range fr.Host {
		if cfg.IsHidden(h.Name) {
			hidden += len(h.Sessions)
			if !all {
				continue
			}
		}
		shownHosts++
		for _, s := range h.Sessions {
			rows = append(rows, r{s, colors.For(s.Name, s.Color)})
		}
	}
	less := func(a, b r) bool { return a.Name < b.Name }
	switch sortMode {
	case "host":
		less = func(a, b r) bool { return a.Host < b.Host || a.Host == b.Host && a.Name < b.Name }
	case "activity":
		less = func(a, b r) bool { return a.Activity > b.Activity }
	case "created":
		less = func(a, b r) bool { return a.Created > b.Created }
	default:
		pi := func(n string) int {
			for i, d := range colors.Palette {
				if d.Name == n {
					return i
				}
			}
			return len(colors.Palette)
		}
		less = func(a, b r) bool { return pi(a.def.Name) < pi(b.def.Name) || a.def.Name == b.def.Name && a.Name < b.Name }
	}
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })

	now := time.Now()
	fmt.Printf("%-2s %-24s %-14s %3s %-3s %-5s %-12s %s\n", "", "SESSION", "HOST", "W", "ATT", "ACT", "TAG", "PREVIEW")
	for _, rw := range rows {
		dot := "\x1b[38;5;" + strconv.Itoa(rw.def.ANSI) + "m●\x1b[0m"
		att := "·"
		if rw.Attached {
			att = "\x1b[32m✓\x1b[0m"
		}
		prev := strings.ReplaceAll(rw.Preview, "\n", " ")
		fmt.Printf("%-2s %-24s %-14s %3d %-3s %-5s %-12s %s\n",
			dot, trunc(rw.Name, 24), trunc(rw.Host, 14), rw.Windows, att, rel(now.Sub(time.Unix(rw.Activity, 0))), trunc(rw.Tag, 12), prev)
	}
	fmt.Printf("\n%d sessions on %d hosts · hub %s\n", len(rows), shownHosts, cfg.HubURL)
	if hidden > 0 && !all {
		fmt.Printf("(+%d on hidden hosts — dtc ls --all to show)\n", hidden)
	}
	return nil
}

func trunc(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

func rel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

// ---- attach / new / kill / color ----

func flagValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

// resolve finds where a named session lives: hub first, local fallback.
// Sessions on hosts marked hidden are ignored (the hub-first path only —
// your own machine is never hidden, so the local fallback always applies).
func resolve(cfg *config.Config, name string) (model.Session, bool) {
	if fr, err := client.FetchFleet(cfg); err == nil {
		var best *model.Session
		for _, h := range fr.Host {
			if cfg.IsHidden(h.Name) {
				continue
			}
			for i := range h.Sessions {
				s := h.Sessions[i]
				if s.Name == name && (best == nil || s.Activity > best.Activity) {
					cp := s
					best = &cp
				}
			}
		}
		if best != nil {
			return *best, true
		}
	}
	if sessions, err := tmux.List(); err == nil {
		for _, s := range sessions {
			if s.Name == name {
				s.Host = cfg.Hostname
				return s, true
			}
		}
	}
	return model.Session{}, false
}

func sshTarget(cfg *config.Config, host string) (string, error) {
	if cfg.IsLocal(host) {
		return "", nil
	}
	alias, ok := cfg.SSHFor(host)
	if !ok || alias == "" {
		return "", fmt.Errorf("no ssh route for host %q in config", host)
	}
	return alias, nil
}

func cmdAttach(args []string) error {
	printOnly := false
	var names []string
	for _, a := range args {
		if a == "--print" {
			printOnly = true
			continue
		}
		names = append(names, a)
	}
	if len(names) != 1 {
		return fmt.Errorf("usage: dtc attach NAME [--print]")
	}
	cfg := loadConfig()
	name := names[0]
	host, _ := flagValue(args, "--host")
	var sess model.Session
	var ok bool
	if host != "" {
		sess = model.Session{Name: name, Host: host}
		ok = true
	} else {
		sess, ok = resolve(cfg, name)
	}
	if !ok {
		return fmt.Errorf("session %q not found in fleet (is the agent running?)", name)
	}
	alias, err := sshTarget(cfg, sess.Host)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	title := colors.For(sess.Name, sess.Color).Emoji + " " + sess.Name
	if alias == "" {
		cmd = tmux.AttachCmd(name)
	} else {
		cmd = exec.Command("ssh", "-t", "-o", "ConnectTimeout=8", alias,
			"tmux new-session -A -s "+tmux.Quote(name))
		title += " · " + sess.Host
	}
	if printOnly {
		fmt.Println(strings.Join(cmd.Args, " "))
		return nil
	}
	if os.Getenv("TMUX") == "" {
		fmt.Fprintf(os.Stdout, "\x1b]2;%s\x07", title)
	}
	return syscall.Exec(cmd.Path, cmd.Args, os.Environ())
}

func cmdNew(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dtc new NAME [--host H]")
	}
	name := args[0]
	cfg := loadConfig()
	host, _ := flagValue(args, "--host")
	if host == "" {
		host = cfg.Hostname
	}
	alias, err := sshTarget(cfg, host)
	if err != nil {
		return err
	}
	if alias == "" {
		err = tmux.NewDetached(name)
	} else {
		out, e := exec.Command("ssh", "-o", "ConnectTimeout=8", alias,
			"tmux new-session -d -s "+tmux.Quote(name)).CombinedOutput()
		err = e
		if err != nil {
			err = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if err != nil {
		return err
	}
	kickAgent(cfg, host, alias)
	fmt.Printf("created %s on %s\n", name, host)
	return nil
}

func cmdKill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dtc kill NAME")
	}
	name := args[0]
	cfg := loadConfig()
	host, _ := flagValue(args, "--host")
	alias := ""
	if host == "" {
		sess, ok := resolve(cfg, name)
		if !ok {
			return fmt.Errorf("session %q not found in fleet", name)
		}
		host = sess.Host
	}
	alias, err := sshTarget(cfg, host)
	if err != nil {
		return err
	}
	if alias == "" {
		err = tmux.Kill(name)
	} else {
		out, e := exec.Command("ssh", "-o", "ConnectTimeout=8", alias,
			"tmux kill-session -t "+tmux.Quote(name)).CombinedOutput()
		err = e
		if err != nil {
			err = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if err != nil {
		return err
	}
	kickAgent(cfg, host, alias)
	fmt.Printf("killed %s on %s\n", name, host)
	return nil
}

func cmdColor(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dtc color NAME <color|auto>")
	}
	name, color := args[0], args[1]
	if color != "auto" {
		if _, ok := colors.ByName(color); !ok {
			return fmt.Errorf("unknown color %q (palette: %s)", color, colorNames())
		}
	}
	cfg := loadConfig()
	host, _ := flagValue(args, "--host")
	if host == "" {
		sess, ok := resolve(cfg, name)
		if !ok {
			return fmt.Errorf("session %q not found in fleet", name)
		}
		host = sess.Host
	}
	alias, err := sshTarget(cfg, host)
	if err != nil {
		return err
	}
	run := func(tmuxArgs ...string) error {
		if alias == "" {
			out, e := exec.Command("tmux", tmuxArgs...).CombinedOutput()
			if e != nil {
				return fmt.Errorf("%v: %s", e, strings.TrimSpace(string(out)))
			}
			return nil
		}
		quoted := make([]string, len(tmuxArgs))
		for i, a := range tmuxArgs {
			quoted[i] = tmux.Quote(a)
		}
		out, e := exec.Command("ssh", "-o", "ConnectTimeout=8", alias, "tmux "+strings.Join(quoted, " ")).CombinedOutput()
		if e != nil {
			return fmt.Errorf("%v: %s", e, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if color == "auto" {
		err = run("set-option", "-u", "-t", name, "@dtc-color")
	} else {
		err = run("set-option", "-t", name, "@dtc-color", color)
	}
	if err == nil {
		def := colors.For(name, color)
		err = run("set-option", "-t", name, "@dtc-title", def.Emoji+" "+name)
	}
	if err != nil {
		return err
	}
	kickAgent(cfg, host, alias)
	fmt.Printf("color of %s: %s\n", name, color)
	return nil
}

func kickAgent(cfg *config.Config, host, alias string) {
	var cmd *exec.Cmd
	if alias == "" {
		self, err := os.Executable()
		if err != nil {
			return
		}
		cmd = exec.Command(self, "agent", "--once")
	} else {
		cmd = exec.Command("ssh", "-o", "ConnectTimeout=8", alias, "bash -lc 'dtc agent --once' 2>/dev/null || true")
	}
	_ = cmd.Run()
}

// ---- agent / hub / init ----

func cmdAgent(args []string) error {
	cfg := loadConfig()
	once := false
	interval := 30 * time.Second
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--once":
			once = true
		case "--interval":
			i++
			if i < len(args) {
				var err error
				interval, err = time.ParseDuration(args[i])
				if err != nil {
					return err
				}
			}
		}
	}
	return agent.Run(cfg, interval, once)
}

func cmdHub(args []string) error {
	addr := "0.0.0.0:7331"
	data := ""
	token := ""
	tokenFile := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			i++
			addr = args[i]
		case "--data":
			i++
			data = args[i]
		case "--token":
			i++
			token = args[i]
		case "--token-file":
			i++
			tokenFile = args[i]
		}
	}
	if token == "" && tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return fmt.Errorf("hub needs --token or --token-file")
	}
	if data == "" {
		data = "/var/lib/dtc/state.json"
	}
	if err := os.MkdirAll(dirOf(data), 0o755); err != nil {
		return err
	}
	store, err := hub.NewStore(data)
	if err != nil {
		return err
	}
	srv := &hub.Server{Token: token, Store: store}
	return srv.ListenAndServe(addr)
}

func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "."
	}
	return p[:i]
}

func cmdInit() error {
	path := config.Path()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	host, _ := os.Hostname()
	tmpl := fmt.Sprintf(`# dtc config — %s
# hub_url + token are machine-local details; this file is gitignored.
hub_url = "http://HUB-IP-OR-HOST:7331"
# token_file = "token"   # default: token next to this file (mode 0600)

# how to reach each fleet host (keys = agent hostnames)
[hosts.%s]
ssh = ""              # this machine

[hosts.main]
ssh = "main"

[hosts.imlal-laptop]
ssh = "imlal-laptop"
`, host, host)
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(tmpl), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nnow put the hub token in %s (mode 0600)\n", path, dirOf(path)+"/token")
	return nil
}
