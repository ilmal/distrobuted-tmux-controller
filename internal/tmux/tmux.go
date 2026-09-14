// Package tmux wraps the tmux CLI: discovery for agent heartbeats and the
// mutations the dashboards perform (locally or over ssh).
package tmux

import (
	"fmt"
	"os"
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
		"#{session_name}", "#{session_attached}",
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
		f := strings.SplitN(line, sep, 7)
		if len(f) < 7 {
			continue
		}
		var created, act int64
		fmt.Sscanf(f[2], "%d", &created)
		fmt.Sscanf(f[3], "%d", &act)
		sessions = append(sessions, model.Session{
			Name:     f[0],
			Attached: f[1] == "1",
			Created:  created,
			Activity: act,
			Color:    f[4],
			Tag:      f[5],
			Title:    f[6],
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

// CurrentSession returns the name of the tmux session this process is running
// inside, or ok=false when it is not running inside tmux at all. It is what
// makes "open this group here" possible — the new windows are siblings of this
// one, in this session.
func CurrentSession() (string, bool) {
	if os.Getenv("TMUX") == "" {
		return "", false
	}
	out, err := run("display-message", "-p", "#{session_name}")
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", false
	}
	return name, true
}

// AttachWindowCmd builds the command that attaches `session` inside a window
// that is already running in this tmux session.
//
// TMUX must be cleared: with it set, tmux refuses the nested attach and the new
// window dies instantly. Clearing it makes the window's pane attach to the
// target session normally, which is exactly the "one tab per session" result.
//
// It returns the command the pane will run, not one this process runs: the
// caller's own environment is irrelevant, because only the panes spawned *by*
// this command inherit what AttachWindowCmd sets up. That is why OpenWindow
// has to be handed the pane's environment separately (see paneEnv).
func AttachWindowCmd(session string) *exec.Cmd {
	cmd := exec.Command("tmux", "new-session", "-A", "-s", session)
	cmd.Env = envWithout("TMUX")
	return cmd
}

// OpenWindow spawns a detached window right after the window `after` (in the
// session `after` belongs to) running attachCmd, and returns the new window id.
//
// `after` is a window id, or a session name for the first window of a group.
// A *session* name resolves to that session's current window every time, so
// opening a whole group against the session would insert each window before the
// last one and the tabs would come out reversed; the caller threads the
// previous window id back in to keep them in the order it listed them.
//
// The pane does not inherit this process's environment — the tmux *server*
// spawns it — so attachCmd's own Env has to be carried across the tmux command
// line. It matters: the server sets TMUX in every pane it creates, and a pane
// that still has it refuses to attach to a session ("sessions should be nested
// with care"), so the window would die the instant it opened. AttachWindowCmd
// clears exactly that variable; dropping its Env here silently discards the fix.
func OpenWindow(after string, attachCmd *exec.Cmd) (string, error) {
	if attachCmd == nil || len(attachCmd.Args) == 0 {
		return "", fmt.Errorf("no attach command")
	}
	argv := strings.Join(quoteAll(attachCmd.Args), " ")
	// The server picks its own socket and passes TMUX to the pane it spawns, so
	// the pane has to clear the variable for itself.
	if len(attachCmd.Env) > 0 {
		argv = "env -u TMUX " + argv
	}
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-a", "-t", after}
	// -S is a global option and has to come before the command name; after it,
	// tmux reads it as an (unknown) new-window flag and quietly ignores it,
	// leaving the window on the default server where this session does not
	// exist. Keep the window on the server this session actually lives on: a
	// tmux started with -L or -S is not on the default socket.
	if dir := socketDir(); dir != "" {
		args = append([]string{"-S", dir}, args...)
	}
	out, err := run(append(args, argv)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// socketDir is the socket the tmux this process is running inside was started
// with, or "" for the default one. TMUX is "<socket-path>,<server-pid>,<index>".
func socketDir() string {
	v := os.Getenv("TMUX")
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i > 0 {
		return v[:i]
	}
	return ""
}

// NameWindow applies the session's tab title to a window just opened in the
// current session, so the tabs read "<emoji> <session>" like everything else.
//
// It sets @dtc-title at *window* scope. install.sh points the terminal tab
// title at that option (set -g set-titles-string '#{@dtc-title}'), which tmux
// re-resolves as you switch windows, so each tab shows the session in it.
// Writing set-titles-string here instead would set a *session* option — every
// tab in the session would then carry the last-opened session's name.
func NameWindow(windowID, title string) error {
	if windowID == "" {
		return nil
	}
	_, err := run("set-option", "-w", "-t", windowID, "@dtc-title", title)
	return err
}

// envWithout copies the process environment without the named variables.
func envWithout(keys ...string) []string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && drop[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = Quote(a)
	}
	return out
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
		want := colors.For(s.Name, s.Color).Title(s.Name)
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
