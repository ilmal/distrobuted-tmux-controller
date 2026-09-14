# dtc — distributed tmux controller

One dashboard for every tmux session across a Tailscale fleet. Sessions on any
machine show up everywhere: name, host, attached state, activity, a live
preview snippet, a color, a tag.

```
🧭 dtc — tmux fleet  · hub ok · refreshed now ago
34 sessions · 5 attached · sort: color
●  SESSION                HOST          ATT  ACT   TAG         PREVIEW
🔵 k8s                    cn1            ·  2h                ...
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
| `V` | switch view: **colors** (grouped, manual order) ⇄ **flat list** (sortable) |
| `o` | open every session in the group you are on as tabs in this tmux session |
| `⇧↑` / `⇧↓` | move the selected session one place inside its color group |
| `P` | manage colors: reorder the groups, rename them |
| `1`–`5` | sort (flat list only): `1` color · `2` host (grouped, local first) · `3` activity · `4` created · `5` name |
| `s`, `S` | cycle sort, reverse |
| `tab` / `⇧tab` | next / previous host tab (`]` / `[` also work) |
| `0` | the “all” tab |
| `6`–`9` | jump straight to the 1st–4th host tab |
| `/` | filter by name/host/tag (`esc` clears) |
| `R`, `?`, `q` | refresh, help, quit |
| `H` | reveal/hide hosts marked `hidden = true` in config (client machines) |

### Host tabs

The fleet is split into **one tab per machine**, created automatically, plus
an `all` tab — so you tab between computers instead of scrolling one flat
list. The tab bar carries per-host session counts (`all 41 · cn1 20 · main 21`)
and is filter-independent, so it always shows where things are. On a
single-host tab the HOST column is dropped and the row gets the width back.

Tabs are ordered local machine first, then alphabetically. A tab whose host
leaves the fleet falls back to `all` rather than showing an empty list.

Client machines (a laptop you sit at, whose sessions are just terminal
windows) can be marked `hidden = true` in `config.toml` — they drop out of
the TUI, `ls`, name resolution, and the hub's web page, with a
`(+N hidden — dtc ls --all)` note so nothing is silently lost. A machine can
also declare *itself* hidden: the agent sends that flag in every heartbeat, so
every dashboard agrees without repeating the setting in each machine's config.
This covers the client machine's **own** view as well — its scratch windows are
the noise the flag exists to remove, and showing them only on the machine you
are least likely to be reading the fleet from is the least useful case. Reveal
with `H` (TUI), `--all` (ls), or `?all=1` (hub page) any time.

Host and color sorts render grouped section headers with per-group counts;
activity freshness is color-coded (green < 5 min, amber < 1 h, dim older).

CLI: `dtc ls [--sort color|host|activity|created|name]`, `dtc attach NAME`,
`dtc new NAME --host H`, `dtc kill NAME`, `dtc color NAME <color|none>`.

## Two views

`V` switches between the two ways of reading the same fleet.

- **colors** — one heading per color, and the order is *yours*: `⇧↑`/`⇧↓`
  move a session inside its group, `P` reorders and renames the groups
  themselves. The `1`–`5` sort keys do nothing here; a manual order and a sort
  key would fight, and the manual one wins.
- **flat list** — one sortable list (`1`–`5`, `s`, `S`). A color is only the
  dot on the left, never a sub-heading.

Because nothing is colored by default, the colors view initially renders as a
single plain list — headings only appear once you actually use two or more
colors.

### Opening a whole color

`o` on a color group opens **every session in it as a new window (tab) of the
tmux session dtc is running in** — dtc's own window plus one tab per session,
each attached. A group that spans machines works too: sessions on this host
become local windows directly, and a remote session's window is a shell running
the same `ssh -t <host> tmux new-session -A -s <name>` that `enter` uses.

dtc has to be running inside tmux for this (there is no window to add to
otherwise); it says so instead of failing silently.

## Colors and Ghostty tabs

Colors, tags and titles are stored as tmux user options (`@dtc-color`,
`@dtc-tag`, `@dtc-title`) **on the owning host** and mirrored to the hub, so
they're consistent no matter which dashboard you use.

There are nine colors, in a deliberately **pastel** palette so a full column of
dots stays calm on a dark terminal:

| | name | emoji |
|---|---|---|
| `217` | red | 🔴 |
| `216` | orange | 🟠 |
| `222` | yellow | 🟡 |
| `157` | green | 🟢 |
| `159` | cyan | 🔷 |
| `153` | blue | 🔵 |
| `183` | purple | 🟣 |
| `218` | pink | 🌸 |
| `250` | gray | ⚪ |

**Nothing is colored until you say so.** A new session starts plain, and the
fleet starts as one list; `c` gives a session a color, `o` opens a whole group,
and `dtc color NAME none` clears one again. A color you set means exactly what
you decided it means.

Renaming a color (`P`, then `r`) changes only its **label** — the key stored on
the session never moves, so a rename can never orphan a session. The palette's
order and labels, and each group's manual session order, live **on the hub**,
not in per-machine config or a tmux option: a color group spans machines, so
one write is what keeps every dashboard grouping the fleet identically.

### Reading a row

```
●  SESSION     HOST   ATT  ACT   TAG    PREVIEW
🔵 k8s         cn1     ·   2h    infra  kubectl get pods
```

- **`ATT`** — whether a client is currently attached.
- **`ACT`** — time since the last activity, colored by freshness (green
  < 5 min, amber < 1 h, gray < 24 h, dim older). This is the row's *only*
  freshness signal; the leading dot is the session's color, not its age.

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
POST /api/v1/heartbeat   agent: {host, tmux_version, os, arch, hidden, sessions[]}
GET  /api/v1/sessions    fleet state + palette + group order
PATCH /api/v1/meta       dashboard: optimistic color/tag patch (pinned 90s)
GET  /api/v1/palette     the fleet's colors in display order, with labels
PUT  /api/v1/palette     replace palette order/labels (must name all nine once)
PUT  /api/v1/order       replace one color group's manual session order
```

`/api/v1/sessions` carries the palette and the order alongside the hosts, so a
dashboard renders the fleet exactly as every other one does from a single
fetch. The two `PUT`s reject payloads that would leave the fleet inconsistent:
a palette that does not name each of the nine colors exactly once is refused,
and an order that names a session that is gone, or one that is no longer in
that group, is dropped rather than stored.
