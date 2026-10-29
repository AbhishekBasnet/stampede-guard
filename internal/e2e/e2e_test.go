//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/app"
	"github.com/AbhishekBasnet/stampede-guard/internal/demoapi"
	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

const adminToken = "dev-admin-token"

type client struct{ t *testing.T }

func (c client) do(method, url, body string) []byte {
	c.t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		c.t.Fatalf("%s %s: status %d: %s", method, url, resp.StatusCode, data)
	}
	return data
}

func TestCampaignEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	demoCfg, err := demoapi.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	demo := demoapi.New(demoCfg, clock.Real{}, log)
	demo.Start(ctx)
	api := httptest.NewServer(demo.Handler())
	defer api.Close()

	var simHandler, cpHandler http.Handler
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { simHandler.ServeHTTP(w, r) }))
	defer sim.Close()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cpHandler.ServeHTTP(w, r) }))
	defer cp.Close()

	cpCfg, err := app.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cpCfg.DBPath = filepath.Join(t.TempDir(), "cp.db")
	cpCfg.TargetHealthURL = api.URL + "/guard/health"
	cpCfg.FCMBaseURL = sim.URL
	cpCfg.HealthPollInterval = 100 * time.Millisecond
	cpCfg.AmpSettle = time.Hour
	cpApp, err := app.New(ctx, cpCfg, clock.Real{}, log)
	if err != nil {
		t.Fatal(err)
	}
	cpHandler = cpApp.Handler()
	if err := cpApp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cpApp.Stop()

	simCfg, err := herdsim.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	simCfg.Devices = 300
	simCfg.APIBaseURL = api.URL
	simCfg.ControlPlaneURL = cp.URL
	simCfg.OpenProbability = 1
	simCfg.OpenDelayMedian = 200 * time.Millisecond
	simCfg.SyncInterval = 5 * time.Second
	s := herdsim.New(simCfg, clock.Real{}, log)
	simHandler = s.Handler()
	s.Start(ctx)

	c := client{t}
	waitFor(t, 20*time.Second, "300 devices registered", func() bool {
		return strings.Contains(string(c.do("GET", cp.URL+"/v1/devices/count", "")), `"count":300`)
	})

	var created struct{ ID string }
	body := `{"name":"e2e","title":"Hi","body":"Go","audience":{"kind":"all"},"pacing":"adaptive","deadline_s":60}`
	if err := json.Unmarshal(c.do("POST", cp.URL+"/v1/campaigns", body), &created); err != nil || created.ID == "" {
		t.Fatalf("create campaign: %v %+v", err, created)
	}
	c.do("POST", cp.URL+"/v1/campaigns/"+created.ID+"/start", "")

	type view struct {
		Status         string
		Targeted, Sent int64
		Failed         int64
	}
	var final view
	var peak int64
	sampleGuard := func() {
		var snap guard.Snapshot
		if err := json.Unmarshal(c.do("GET", api.URL+"/guard/health", ""), &snap); err != nil {
			t.Fatal(err)
		}
		peak = max(peak, snap.CampaignRequests[created.ID])
	}
	waitFor(t, 60*time.Second, "campaign completed", func() bool {
		sampleGuard()
		if err := json.Unmarshal(c.do("GET", cp.URL+"/v1/campaigns/"+created.ID, ""), &final); err != nil {
			t.Fatal(err)
		}
		return final.Status == "completed"
	})
	t.Logf("campaign: %+v", final)
	if final.Sent != 300 || final.Failed != 0 {
		t.Fatalf("sent %d failed %d, want 300 0", final.Sent, final.Failed)
	}

	waitFor(t, 15*time.Second, "guard counted more than 300 campaign requests", func() bool {
		sampleGuard()
		return peak > 300
	})
	t.Logf("peak guard campaign_requests[%s] = %d", created.ID, peak)

	var ticks struct{ Ticks []json.RawMessage }
	if err := json.Unmarshal(c.do("GET", cp.URL+"/v1/campaigns/"+created.ID+"/ticks", ""), &ticks); err != nil || len(ticks.Ticks) == 0 {
		t.Fatalf("pacer ticks: %v %d", err, len(ticks.Ticks))
	}
	t.Logf("pacer ticks: %d", len(ticks.Ticks))
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for: %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
