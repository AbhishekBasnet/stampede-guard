package herdsim

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

type lockedRand struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (l *lockedRand) Float64() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Float64()
}

func (l *lockedRand) NormFloat64() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.NormFloat64()
}

func (l *lockedRand) IntN(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.IntN(n)
}

type metrics struct {
	requests   *prometheus.CounterVec
	retries    *prometheus.CounterVec
	suppressed *prometheus.CounterVec
	pushes     prometheus.Counter
	opens      prometheus.Counter
	orders     *prometheus.CounterVec
}

type Sim struct {
	cfg   Config
	rt    *runtimeConfig
	clk   clock.Clock
	log   *slog.Logger
	stats *Stats

	devices []*Device
	byToken map[string]*Device

	client *http.Client
	sem    chan struct{}
	sleep  func(ctx context.Context, d time.Duration) error
	rnd    *lockedRand

	onPush   func(d *Device, campaignID string)
	registry *prometheus.Registry
	met      metrics
	ctx      context.Context
}

func New(cfg Config, clk clock.Clock, log *slog.Logger) *Sim {
	s := &Sim{
		cfg:   cfg,
		rt:    &runtimeConfig{cfg: RuntimeConfig{NaiveFraction: cfg.NaiveFraction, SyncMode: cfg.SyncMode, PrefetchOnPush: cfg.PrefetchOnPush}},
		clk:   clk,
		log:   log,
		stats: &Stats{},
		sem:   make(chan struct{}, cfg.MaxConcurrent),
		sleep: realSleep,
		rnd:   &lockedRand{r: rand.New(rand.NewPCG(uint64(clk.Now().UnixNano()), 0x5a17))},
		ctx:   context.Background(),
		client: &http.Client{Transport: &http.Transport{
			MaxIdleConns: cfg.MaxConcurrent, MaxIdleConnsPerHost: cfg.MaxConcurrent, IdleConnTimeout: 30 * time.Second,
		}},
		byToken:  make(map[string]*Device, cfg.Devices),
		registry: prometheus.NewRegistry(),
	}
	s.onPush = s.handlePush
	s.devices = make([]*Device, cfg.Devices)
	for i := range s.devices {
		d := NewDevice(i + 1)
		d.SetNaiveFraction(cfg.NaiveFraction)
		s.devices[i] = d
		s.byToken[d.Token] = d
	}
	s.initMetrics()
	return s
}

func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Sim) initMetrics() {
	s.met = metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sim_requests_total", Help: "HTTP attempts sent to the demo API.",
		}, []string{"kind", "priority", "outcome"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sim_retries_total", Help: "Retries issued.",
		}, []string{"kind"}),
		suppressed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sim_suppressed_total", Help: "Requests a guarded client skipped because of the fleet policy.",
		}, []string{"priority"}),
		pushes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sim_pushes_received_total", Help: "Pushes delivered to virtual phones.",
		}),
		opens: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sim_app_opens_total", Help: "App sessions started.",
		}),
		orders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sim_orders_total", Help: "Orders attempted by outcome.",
		}, []string{"outcome"}),
	}
	s.registry.MustRegister(s.met.requests, s.met.retries, s.met.suppressed, s.met.pushes, s.met.opens, s.met.orders)
}

func (s *Sim) SetNaiveFraction(f float64) {
	for _, d := range s.devices {
		d.SetNaiveFraction(f)
	}
}

func (s *Sim) NaiveCount() int {
	n := 0
	for _, d := range s.devices {
		if d.Naive() {
			n++
		}
	}
	return n
}

func (s *Sim) Devices() []*Device { return s.devices }
