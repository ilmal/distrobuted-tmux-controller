// Package client is the hub client used by the agent and the dashboards.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

var httpc = &http.Client{Timeout: 6 * time.Second}

func request(cfg *config.Config, method, path string, body any, out any) error {
	if cfg.HubURL == "" {
		return fmt.Errorf("no hub_url configured (run `dtc init`)")
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, cfg.HubURL+path, rd)
	if err != nil {
		return err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("hub %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func FetchFleet(cfg *config.Config) (*model.FleetResponse, error) {
	var fr model.FleetResponse
	if err := request(cfg, http.MethodGet, "/api/v1/sessions", nil, &fr); err != nil {
		return nil, err
	}
	return &fr, nil
}

func PatchMeta(cfg *config.Config, p model.MetaPatch) error {
	return request(cfg, http.MethodPatch, "/api/v1/meta", p, nil)
}

// PutPalette stores the fleet's color labels and display order.
func PutPalette(cfg *config.Config, entries []model.PaletteEntry) error {
	return request(cfg, http.MethodPut, "/api/v1/palette", model.Palette{Colors: entries}, nil)
}

// PutOrder stores one color group's manual session ordering.
func PutOrder(cfg *config.Config, o model.GroupOrder) error {
	return request(cfg, http.MethodPut, "/api/v1/order", o, nil)
}

func PostHeartbeat(cfg *config.Config, hb model.Heartbeat) error {
	return request(cfg, http.MethodPost, "/api/v1/heartbeat", hb, nil)
}
