# dtc — distributed tmux controller

One dashboard for every tmux session across a Tailscale fleet. Sessions on any
machine show up everywhere: name, host, window count, attached state, activity,
a live preview snippet, a color, a tag.

```
🧭 dtc — tmux fleet  · hub ok · refreshed now ago
34 sessions (34 shown) · 5 attached · sort: color
●  SESSION                HOST         W  ATT  ACT   TAG         PREVIEW
🔵 k8s                    cn1          1    ·  2h                ...
```

## Architecture

```
 laptop ──agent──┐                     ┌──HTML page── phone/browser
 main   ──agent──┼── heartbeat ──▶ hub (k8s on cn1, JSON state)
 cn1    ──agent──┘   every 30s       └──/api/v1/*── dtc TUI / ls / attach
                                          │
                                          └─ ssh (or local tmux) for
                                             attach · preview · mutations
```

- **hub** (`dtc hub`) — the single source of *current fleet state*. Tiny Go
  HTTP service + one JSON file. Deployed on k8s with a hostPort, reachable at
  `http://<hub-host>:7331`. Also serves a zero-JS status page at `/`.
- **agent** (`dtc agent`) — runs on every machine (systemd user service).
  Every 30s it reads local tmux and posts a heartbeat. It also keeps a
  `@dtc-title` user option in sync so tmux sets the terminal tab title to
  `<color emoji> <session name>`.
- **dashboard** (`dtc`) — bubbletea TUI. Reads the hub for fleet state,
  performs actions over ssh (or local tmux), then kicks the host's agent for
  an instant re-sync. Falls back to a local-only view if the hub is down.

## Security model (public repo)

Everything machine-specific lives in **gitignored per-machine files** —
`~/.config/dtc/config.toml` + `token`. The repo contains only placeholders
(`config.example.toml`). Auth is a single bearer token shared by the fleet;
the hub listens on the Tailscale interface, so the token is a second layer,
not the only one.

## Install a machine

```bash
git clone https://github.com/ilmal/distrobuted-tmux-controller && cd distrobuted-tmux-controller
go build -o dtc ./cmd/dtc
./install.sh http://<hub-host>:7331 <token>   # binary, config, agent, tmux titles
```

`install.sh` writes the real hub URL and token only to `~/.config/dtc/`,
never prints them, and never touches the repo.

Hub deployment lives in `deploy/` (k8s manifest + Dockerfile). The token is
applied out-of-band: `kubectl -n dtc create secret generic dtc-token --from-file=token=...`.

## Dashboard keys

| | |
|---|---|
| `enter` | attach (local tmux, or ssh + `tmux new-session -A`) |
| `p` | live pane preview (3000 lines, scrollable, session-colored border) |
| `c` / `t` / `r` | set color / tag / rename (applies on the owning host) |
| `n` / `K` | new session on any host / kill (with confirm) |
| `1`–`5` | sort: `1` color · `2` host (grouped, local first) · `3` activity · `4` created · `5` name |
| `s`, `S` | cycle sort, reverse |
| `/` | filter by name/host/tag (`esc` clears) |
| `R`, `?`, `q` | refresh, help, quit |
| `H` | reveal/hide hosts marked `hidden = true` in config (client machines) |

Client machines (a laptop you sit at, whose sessions are just terminal
windows) can be marked `hidden = true` in `config.toml` — they drop out of
the TUI, `ls`, and name resolution, with a `(+N hidden — dtc ls --all)`
note so nothing is silently lost. The local machine is never hidden from
itself; reveal with `H` (TUI) or `--all` (ls) any time.

Host and color sorts render grouped section headers with per-group counts;
activity freshness is color-coded (green < 5 min, amber < 1 h, dim older).

CLI: `dtc ls [--sort color|host|activity|created|name]`, `dtc attach NAME`,
`dtc new NAME --host H`, `dtc kill NAME`, `dtc color NAME <color|auto>`.

## Colors and Ghostty tabs

Colors, tags and titles are stored as tmux user options (`@dtc-color`,
`@dtc-tag`, `@dtc-title`) **on the owning host** and mirrored to the hub, so
they're consistent no matter which dashboard you use. Unset colors get a
deterministic color from the session name.

Ghostty (as of 1.3.x) has **no tab-color escape sequence** — the OSC 6 PR was
rejected upstream ([#12858](https://github.com/ghostty-org/ghostty/pull/12858)),
tab color via keybind/AppleScript is still open
([#11498](https://github.com/ghostty-org/ghostty/issues/11498)). So dtc puts
the color in the tab *title* as an emoji dot (🔴🟠🟡🟢🔷🔵🟣🌸⚪) via
`set-titles` — the tab reads e.g. `🔴 lawcrawl`. When Ghostty grows a real
tab-color OSC, the agent is the single place to add it.

## API

```
GET  /healthz            liveness (no auth)
GET  /                   HTML status page (no auth)
POST /api/v1/heartbeat   agent: {host, tmux_version, os, arch, sessions[]}
GET  /api/v1/sessions    fleet state
PATCH /api/v1/meta       dashboard: optimistic color/tag patch (pinned 90s)
```
