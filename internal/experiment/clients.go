package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

type Clients struct {
	APIURL, CPURL, SimURL, Token string
	HTTP                         *http.Client
}

func NewClients(api, cp, sim, token string) *Clients {
	return &Clients{
		APIURL: strings.TrimRight(api, "/"), CPURL: strings.TrimRight(cp, "/"), SimURL: strings.TrimRight(sim, "/"),
		Token: token, HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Clients) call(ctx context.Context, method, target string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request for %s: %w", target, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, target, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, target, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %.200s", method, target, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, target, err)
		}
	}
	return nil
}

func (c *Clients) SetEnforce(ctx context.Context, enabled bool) error {
	return c.call(ctx, http.MethodPost, c.APIURL+"/guard/enforce", map[string]bool{"enabled": enabled}, nil)
}

func (c *Clients) ResetAPI(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, c.APIURL+"/admin/reset", nil, nil)
}

func (c *Clients) SetChaos(ctx context.Context, spec ChaosSpec) error {
	return c.call(ctx, http.MethodPost, c.APIURL+"/admin/chaos", map[string]any{
		"kind": spec.Kind, "extra_ms": spec.ExtraMs, "duration_s": int(spec.Duration.Seconds()),
	}, nil)
}

func (c *Clients) Health(ctx context.Context) (guard.Snapshot, error) {
	var snap guard.Snapshot
	err := c.call(ctx, http.MethodGet, c.APIURL+"/guard/health", nil, &snap)
	return snap, err
}

func (c *Clients) ConfigureSim(ctx context.Context, naiveFraction float64, syncMode string) error {
	return c.call(ctx, http.MethodPost, c.SimURL+"/sim/config", map[string]any{
		"naive_fraction": naiveFraction, "sync_mode": syncMode, "prefetch_on_push": true,
	}, nil)
}

func (c *Clients) SetSyncMode(ctx context.Context, mode string) error {
	return c.call(ctx, http.MethodPost, c.SimURL+"/sim/config", map[string]any{"sync_mode": mode}, nil)
}

func (c *Clients) ResetSim(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, c.SimURL+"/sim/reset", nil, nil)
}

func (c *Clients) SimStats(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.call(ctx, http.MethodGet, c.SimURL+"/sim/stats", nil, &raw)
	return raw, err
}

func (c *Clients) Timeline(ctx context.Context, since int64) ([]herdsim.Second, error) {
	var out struct {
		Seconds []herdsim.Second `json:"seconds"`
	}
	q := url.Values{"since": {strconv.FormatInt(since, 10)}}
	err := c.call(ctx, http.MethodGet, c.SimURL+"/sim/timeline?"+q.Encode(), nil, &out)
	return out.Seconds, err
}

func (c *Clients) CreateCampaign(ctx context.Context, sc Scenario) (string, error) {
	req := campaign.CreateRequest{
		Name: sc.ID, Title: "Stampede Guard experiment " + sc.ID, Body: sc.Name,
		Audience: campaign.Audience{Kind: campaign.AudienceAll}, Pacing: campaign.Pacing(sc.Pacing),
		StaticRate: sc.StaticRate, DeadlineSeconds: 300,
	}
	var v campaign.View
	if err := c.call(ctx, http.MethodPost, c.CPURL+"/v1/campaigns", req, &v); err != nil {
		return "", err
	}
	return v.ID, nil
}

func (c *Clients) StartCampaign(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, c.CPURL+"/v1/campaigns/"+id+"/start", nil, nil)
}

func (c *Clients) Campaign(ctx context.Context, id string) (campaign.View, error) {
	var v campaign.View
	err := c.call(ctx, http.MethodGet, c.CPURL+"/v1/campaigns/"+id, nil, &v)
	return v, err
}
