package herdsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
)

const maxBodyBytes = 1 << 16

func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	admin := func(h http.HandlerFunc) http.Handler { return httpx.RequireBearer(s.cfg.AdminToken, h) }

	mux.Handle("POST /v1/projects/{project}/{action}", s.fcmHandler())
	mux.Handle("GET /sim/stats", admin(s.handleStats))
	mux.Handle("GET /sim/timeline", admin(s.handleTimeline))
	mux.Handle("POST /sim/config", admin(s.handleConfig))
	mux.Handle("POST /sim/reset", admin(s.handleReset))
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

type configView struct {
	RuntimeConfig
	SyncIntervalS       float64 `json:"sync_interval_s"`
	OpenProbability     float64 `json:"open_probability"`
	OpenDelayMedianMs   int64   `json:"open_delay_median_ms"`
	OpenDelaySigma      float64 `json:"open_delay_sigma"`
	OrderProbability    float64 `json:"order_probability"`
	OrganicOpensPerHour float64 `json:"organic_opens_per_hour"`
}

func (s *Sim) configView() configView {
	return configView{
		RuntimeConfig: s.rt.get(), SyncIntervalS: s.cfg.SyncInterval.Seconds(), OpenProbability: s.cfg.OpenProbability,
		OpenDelayMedianMs: s.cfg.OpenDelayMedian.Milliseconds(), OpenDelaySigma: s.cfg.OpenDelaySigma,
		OrderProbability: s.cfg.OrderProbability, OrganicOpensPerHour: s.cfg.OrganicOpensPerHour,
	}
}

func (s *Sim) handleStats(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"devices": len(s.devices), "naive_devices": s.NaiveCount(), "config": s.configView(),
		"totals": s.stats.Totals(), "last_60s": s.stats.Last(60),
	})
}

func (s *Sim) handleTimeline(w http.ResponseWriter, r *http.Request) {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", "since must be unix seconds")
			return
		}
		since = n
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"seconds": s.stats.Since(since)})
}

func (s *Sim) handleConfig(w http.ResponseWriter, r *http.Request) {
	var patch RuntimePatch
	if err := httpx.ReadJSON(w, r, &patch, maxBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := patch.validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.rt.apply(patch)
	if patch.NaiveFraction != nil {
		s.SetNaiveFraction(*patch.NaiveFraction)
	}
	httpx.WriteJSON(w, http.StatusOK, s.configView())
}

func (s *Sim) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.stats.Reset()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sim) Register(ctx context.Context) error {
	type item struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
		AppBuild int    `json:"app_build"`
	}
	for start := 0; start < len(s.devices); start += s.cfg.RegisterBatch {
		end := min(start+s.cfg.RegisterBatch, len(s.devices))
		items := make([]item, 0, end-start)
		for _, d := range s.devices[start:end] {
			items = append(items, item{d.Token, d.Platform, d.AppBuild})
		}
		body, err := json.Marshal(map[string]any{"devices": items})
		if err != nil {
			return fmt.Errorf("encode device batch: %w", err)
		}
		if err := s.postBatch(ctx, body); err != nil {
			return err
		}
	}
	s.log.Info("devices registered", "count", len(s.devices))
	return nil
}

func (s *Sim) postBatch(ctx context.Context, body []byte) error {
	var last error
	for attempt := 0; attempt < registerAttempts; attempt++ {
		if attempt > 0 {
			if err := s.sleep(ctx, registerRetryEvery); err != nil {
				return err
			}
		}
		last = s.tryBatch(ctx, body)
		if last == nil {
			return nil
		}
		s.log.Warn("device registration failed, retrying", "error", last)
	}
	return fmt.Errorf("register devices after %d attempts: %w", registerAttempts, last)
}

func (s *Sim) tryBatch(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.ControlPlaneURL+"/v1/devices/batch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (s *Sim) Start(ctx context.Context) {
	s.ctx = ctx
	go s.stats.run(ctx.Done(), s.clk.Now)
	go func() {
		if err := s.Register(ctx); err != nil {
			if ctx.Err() == nil {
				s.log.Error("registration failed", "error", err)
			}
			return
		}
		for _, d := range s.devices {
			go s.syncLoop(ctx, d)
		}
		go s.organicLoop(ctx)
	}()
}

func (s *Sim) Run(ctx context.Context) error {
	srv := httpx.NewServer(s.cfg.Listen, s.Handler())
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("herd-sim starting", "listen", s.cfg.Listen, "devices", len(s.devices), "naive_fraction", s.rt.get().NaiveFraction,
		"sync_mode", s.rt.get().SyncMode, "api_base_url", s.cfg.APIBaseURL)
	s.Start(ctx)

	select {
	case err := <-errc:
		return fmt.Errorf("serve %s: %w", s.cfg.Listen, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
