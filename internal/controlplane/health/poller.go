package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

type result struct {
	snap      guard.Snapshot
	fetchedAt time.Time
}

type Poller struct {
	url        string
	token      string
	interval   time.Duration
	staleAfter time.Duration
	client     *http.Client
	clock      clock.Clock
	log        *slog.Logger
	latest     atomic.Pointer[result]
	pollErrors prometheus.Counter
}

func NewPoller(url, token string, interval, timeout, staleAfter time.Duration, clk clock.Clock, log *slog.Logger) *Poller {
	return &Poller{
		url:        url,
		token:      token,
		interval:   interval,
		staleAfter: staleAfter,
		client:     &http.Client{Timeout: timeout},
		clock:      clk,
		log:        log,
		pollErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cp_health_poll_errors_total", Help: "Failed polls of the target's guard health endpoint.",
		}),
	}
}

func (p *Poller) RegisterMetrics(reg prometheus.Registerer) error {
	stale := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "cp_health_stale", Help: "1 when the latest target health is missing or too old.",
	}, func() float64 {
		if _, _, stale := p.Latest(); stale {
			return 1
		}
		return 0
	})
	for _, c := range []prometheus.Collector{p.pollErrors, stale} {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("register health metrics: %w", err)
		}
	}
	return nil
}

func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

// A failed poll keeps the previous snapshot; Latest reports it as stale once it is too old.
func (p *Poller) pollOnce(ctx context.Context) {
	snap, err := p.fetch(ctx)
	if err != nil {
		p.pollErrors.Inc()
		p.log.Debug("health poll failed", "url", p.url, "error", err)
		return
	}
	p.latest.Store(&result{snap: snap, fetchedAt: p.clock.Now()})
}

func (p *Poller) fetch(ctx context.Context) (guard.Snapshot, error) {
	var snap guard.Snapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return snap, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return snap, fmt.Errorf("get %s: %w", p.url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return snap, fmt.Errorf("get %s: status %d", p.url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, fmt.Errorf("decode snapshot from %s: %w", p.url, err)
	}
	return snap, nil
}

func (p *Poller) Latest() (snap guard.Snapshot, fetchedAt time.Time, stale bool) {
	r := p.latest.Load()
	if r == nil {
		return guard.Snapshot{}, time.Time{}, true
	}
	return r.snap, r.fetchedAt, p.clock.Now().Sub(r.fetchedAt) > p.staleAfter
}
