// Package tmux wraps the tmux CLI: discovery for agent heartbeats and the
// mutations the dashboards perform (locally or over ssh).
package tmux

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

const sep = "\x1f"

var (
	ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-B]")
	ctlRe  = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
)

// Available reports whether a tmux binary exists and a server is running.
func Available() bool {
	p, err := exec.LookPath("tmux")
	return err == nil && exec.Command(p, "ls").Run() == nil
}

func Version() string {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func run(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// List returns raw session metadata for the local server (no previews).
func List() ([]model.Session, error) {
	out, err := run("list-sessions", "-F", strings.Join([]string{
		"#{session_name}", "#{session_windows}", "#{session_attached}",
		"#{session_created}", "#{session_activity}",
		"#{@dtc-color}", "#{@dtc-tag}", "#{@dtc-title}",
	}, sep))
	if err != nil {
		return nil, err
	}
	// tmux >= 3.3 escapes control characters in format output, so the 0x1f
	// separator arrives as literal backslash-octal text there (tmux 3.2
	// passes the raw byte). Normalize before parsing.
	out = strings.ReplaceAll(out, `\037`, sep)
	var sessions []model.Session
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, sep, 8)
		if len(f) < 8 {
			continue
		}
		var ws, created, act int64
		fmt.Sscanf(f[1], "%d", &ws)
		fmt.Sscanf(f[3], "%d", &created)
		fmt.Sscanf(f[4], "%d", &act)
		sessions = append(sessions, model.Session{
			Name:     f[0],
			Windows:  int(ws),
			Attached: f[2] == "1",
			Created:  created,
			Activity: act,
			Color:    f[5],
			Tag:      f[6],
			Title:    f[7],
		})
	}
	return sessions, nil
}

// Capture returns the last `lines` lines of a session's active pane.
func Capture(name string, lines int) (string, error) {
	out, err := run("capture-pane", "-t", name, "-p", "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// Preview builds the short multi-line snippet sent in heartbeats.
func Preview(name string) string {
	raw, err := Capture(name, 8)
	if err != nil {
		return ""
	}
	var kept []string
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimSpace(Sanitize(ln))
		if ln == "" {
			continue
		}
		kept = append(kept, ln)
		if len(kept) == 5 {
			break
		}
	}
	s := strings.Join(kept, " ⏎ ")
	if len(s) > 240 {
		s = s[:237] + "…"
	}
	return s
}

// Sanitize strips terminal escape sequences and control characters.
func Sanitize(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = ctlRe.ReplaceAllString(s, "")
	return s
}

// HeartbeatSessions collects local sessions (with previews) and syncs the
// @dtc-title user option so tmux's set-titles shows "<emoji> <name>" in the
// terminal tab. Returns nil when no tmux server is running.
func HeartbeatSessions() []model.Session {
	if !Available() {
		return nil
	}
	sessions, err := List()
	if err != nil {
		return nil
	}
	var args []string
	for i := range sessions {
		s := &sessions[i]
		s.Preview = Preview(s.Name)
		want := colors.For(s.Name, s.Color).Emoji + " " + s.Name
		if s.Title != want {
			args = append(args, "set-option", "-t", s.Name, "@dtc-title", want, ";")
		}
	}
	if len(args) > 0 {
		_, _ = run(args[:len(args)-1]...) // drop trailing ";"
	}
	return sessions
}

// SetOption sets a per-session user option, locally.
func SetOption(name, key, val string) error {
	_, err := run("set-option", "-t", name, key, val)
	return err
}

// DeleteOption removes a per-session user option, locally.
func DeleteOption(name, key string) error {
	_, err := run("set-option", "-u", "-t", name, key)
	return err
}

func Rename(old, new string) error {
	_, err := run("rename-session", "-t", old, new)
	return err
}

func Kill(name string) error {
	_, err := run("kill-session", "-t", name)
	return err
}

func NewDetached(name string) error {
	_, err := run("new-session", "-d", "-s", name)
	return err
}

// AttachCmd builds the interactive attach-or-create command for a session.
func AttachCmd(name string) *exec.Cmd {
	return exec.Command("tmux", "new-session", "-A", "-s", name)
}

// Quote makes a word safe as part of a remote shell command line.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("_.,:@/=-", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}
