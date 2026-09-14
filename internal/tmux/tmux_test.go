package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTmux puts a stand-in tmux first on PATH that records the argv it was
// called with, so the exact command line OpenWindow builds can be asserted
// without a real tmux server. It returns the path of that recording.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\nprintf '@99\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func recorded(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The pane running the attach must clear TMUX for itself: the tmux server sets
// it in every pane it creates, and a pane that still has it refuses to attach,
// so the window would die the instant it opened. Clearing AttachWindowCmd's Env
// without carrying it onto the command line loses exactly that.
func TestOpenWindowClearsTmuxInThePane(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "")

	id, err := OpenWindow("uitest", AttachWindowCmd("probe-a"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "@99" {
		t.Errorf("window id = %q, want %q", id, "@99")
	}
	got := recorded(t, log)
	if !strings.Contains(got, "env -u TMUX tmux new-session -A -s probe-a") {
		t.Errorf("pane command does not clear TMUX:\n%s", got)
	}
	// Right after the current window, so a group opens in listed order.
	if !strings.Contains(got, "-a\n-t\nuitest\n") {
		t.Errorf("window is not placed after the current one:\n%s", got)
	}
}

// A tmux started on its own socket must open the group on that same server —
// the default one does not have this session at all.
//
// -S must precede the command name: after it, tmux reads it as an unknown
// new-window flag and silently ignores it, so the window lands on the default
// server and the "open a whole color" key appears to do nothing.
func TestOpenWindowKeepsTheServerSocket(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "/tmp/probe.sock,4242,0")

	if _, err := OpenWindow("uitest", AttachWindowCmd("probe-a")); err != nil {
		t.Fatal(err)
	}
	got := recorded(t, log)
	if !strings.Contains(got, "-S\n/tmp/probe.sock\n") {
		t.Fatalf("socket not passed through to tmux:\n%s", got)
	}
	sAt := strings.Index(got, "-S\n")
	if cmdAt := strings.Index(got, "new-window\n"); cmdAt < sAt {
		t.Errorf("-S must come before the command name, got:\n%s", got)
	}
}

// The default server is selected by leaving -S out entirely.
func TestOpenWindowWithoutSocketOmitsS(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "")

	if _, err := OpenWindow("uitest", AttachWindowCmd("probe-a")); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, log); strings.Contains(got, "\n-S\n") {
		t.Errorf("unexpected -S on the default socket:\n%s", got)
	}
}

func TestAttachWindowCmdDropsTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/probe.sock,4242,0")

	cmd := AttachWindowCmd("probe-a")
	if cmd.Args[0] != "tmux" || !strings.HasSuffix(strings.Join(cmd.Args, " "), "new-session -A -s probe-a") {
		t.Errorf("unexpected argv: %v", cmd.Args)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "TMUX=") {
			t.Fatalf("TMUX not cleared from the pane environment: %v", cmd.Env)
		}
	}
	// envWithout must not have thrown the rest of the environment away.
	if _, ok := os.LookupEnv("PATH"); ok && len(cmd.Env) == 0 {
		t.Error("pane environment is empty; expected everything but TMUX")
	}
}

// socketDir parses TMUX="<socket>,<pid>,<index>". A malformed value falls back
// to the default server rather than a bogus -S path.
func TestSocketDir(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"/tmp/probe.sock,4242,0", "/tmp/probe.sock"},
		{"no-comma", ""},
		{",4242,0", ""},
	} {
		t.Setenv("TMUX", tc.in)
		if got := socketDir(); got != tc.want {
			t.Errorf("socketDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// OpenWindow is also used with a plain ssh command, which brings no Env of its
// own; the TMUX clearing must not leak into that case.
func TestOpenWindowLeavesAForeignCommandAlone(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "")

	cmd := exec.Command("ssh", "-t", "cn1", "tmux new-session -A -s remote")
	if _, err := OpenWindow("uitest", cmd); err != nil {
		t.Fatal(err)
	}
	got := recorded(t, log)
	if strings.Contains(got, "env -u TMUX") {
		t.Errorf("ssh command should be run as-is:\n%s", got)
	}
	if !strings.Contains(got, "ssh -t cn1") {
		t.Errorf("ssh command missing from the window:\n%s", got)
	}
}
