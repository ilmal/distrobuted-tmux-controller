#!/usr/bin/env bash
# dtc installer — run ON the machine being enrolled.
#
#   ./install.sh <hub-url> <token>
#
# Installs: binary (./dtc next to this script), per-machine config + token,
# agent (systemd user service, cron fallback), and the tmux set-titles block.
# Machine-local values (hub IP, token) are written to gitignored files only.
set -euo pipefail

HUB_URL="${1:?usage: install.sh <hub-url> <token>}"
TOKEN="${2:?usage: install.sh <hub-url> <token>}"
BIN="$(dirname "$0")/dtc"

[[ -x "$BIN" ]] || { echo "dtc binary not found next to install.sh" >&2; exit 1; }

# 1. binary → ~/.local/bin (works without root)
mkdir -p ~/.local/bin
install -m 755 "$BIN" ~/.local/bin/dtc
export PATH="$HOME/.local/bin:$PATH"

# 2. config + token (machine-local, never committed)
CFG_DIR=~/.config/dtc
mkdir -p "$CFG_DIR"
HOSTNAME_DTC="$(hostname)"
[[ -f "$CFG_DIR/config.toml" ]] || cat > "$CFG_DIR/config.toml" <<EOF
# machine-local dtc config — gitignored, contains tailnet details
hub_url = "$HUB_URL"

[hosts.$HOSTNAME_DTC]
ssh = ""
EOF
chmod 600 "$CFG_DIR/config.toml"
printf '%s' "$TOKEN" > "$CFG_DIR/token"
chmod 600 "$CFG_DIR/token"

# 3. agent — systemd user service
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/dtc-agent.service <<EOF
[Unit]
Description=dtc agent — tmux fleet heartbeat

[Service]
ExecStart=$HOME/.local/bin/dtc agent --interval 30s
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload || true
systemctl --user enable --now dtc-agent.service 2>/dev/null || true
loginctl enable-linger "$USER" 2>/dev/null || true

AGENT_RUNNING=$(systemctl --user is-active dtc-agent.service 2>/dev/null || true)
if [[ "$AGENT_RUNNING" != "active" ]]; then
  # fallback: cron (starts at login/boot, flock prevents overlap)
  CRON_LINE='* * * * * $HOME/.local/bin/dtc agent --once 2>/dev/null'
  ( crontab -l 2>/dev/null | grep -vF 'dtc agent --once'; echo "$CRON_LINE" ) | crontab -
  nohup "$HOME/.local/bin/dtc" agent --interval 30s >/dev/null 2>&1 &
  echo "systemd user service unavailable — installed cron fallback"
fi

# 4. tmux: titles follow the session name + color emoji (Ghostty tabs)
TMUXRC=~/.tmux.conf
touch "$TMUXRC"
if ! grep -q '# >>> dtc >>>' "$TMUXRC"; then
  cat >> "$TMUXRC" <<'EOF'

# >>> dtc >>> (distributed-tmux-controller: tab title = colored session name)
set -g set-titles on
set -g set-titles-string '#{@dtc-title}'
# <<< dtc <<<
EOF
fi
tmux source-file "$TMUXRC" 2>/dev/null || true

echo "dtc installed. verify: dtc ls"
