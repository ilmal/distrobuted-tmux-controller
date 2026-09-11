// Package agent runs on every machine, heartbeating local tmux state to the
// hub. One-shot mode (--once) is used by cron and by dashboards to get a
// fresh authoritative view right after a mutation.
package agent

import (
	"log"
	"runtime"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/client"
	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
	"github.com/ilmal/distrobuted-tmux-controller/internal/tmux"
)

func Run(cfg *config.Config, interval time.Duration, once bool) error {
	log.SetFlags(0)
	for {
		if err := beat(cfg); err != nil {
			log.Printf("agent: %v", err)
		}
		if once {
			return nil
		}
		time.Sleep(interval)
	}
}

func beat(cfg *config.Config) error {
	hb := model.Heartbeat{
		Host:        cfg.Hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		TmuxVersion: tmux.Version(),
		Sessions:    tmux.HeartbeatSessions(),
	}
	return client.PostHeartbeat(cfg, hb)
}
