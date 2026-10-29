package demoapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/ids"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

const (
	maxChaosExtraMs   = 10000
	maxChaosDurationS = 600
	maxBodyBytes      = 1 << 20
)

type Server struct {
	cfg         Config
	log         *slog.Logger
	guard       *guard.Guard
	chaos       *Chaos
	capacity    *Capacity
	poolTimeout prometheus.Counter
	handler     http.Handler
	metrics     *prometheus.Registry
}

func New(cfg Config, clk clock.Clock, log *slog.Logger) *Server {
	reg := prometheus.NewRegistry()
	chaos := NewChaos(clk)
	s := &Server{
		cfg:      cfg,
		log:      log,
		chaos:    chaos,
		capacity: NewCapacity(cfg.DBConns, cfg.DBWaitTimeout, cfg.DBRespectCancel, chaos),
		poolTimeout: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "demo_db_pool_timeouts_total", Help: "Requests that gave up waiting for a DB connection.",
		}),
		metrics: reg,
	}

	gcfg := cfg.Guard
	gcfg.AdminToken = cfg.AdminToken
	gcfg.RoutePriorities = map[string]policy.Priority{
		"GET /v1/feed":          policy.PriorityUser,
		"GET /v1/profile":       policy.PriorityUser,
		"GET /v1/notifications": policy.PriorityUser,
		"GET /v1/prefetch":      policy.PriorityPrefetch,
		"POST /v1/sync":         policy.PriorityBackground,
		"POST /v1/orders":       policy.PriorityCritical,
	}
	s.guard = guard.New(gcfg, clk, reg, log)

	collectors := []prometheus.Collector{
		s.poolTimeout,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "demo_db_pool_in_use", Help: "DB connections currently held.",
		}, func() float64 { return float64(s.capacity.InUse()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "demo_chaos_active", Help: "1 while a chaos fault is active.",
		}, func() float64 {
			if s.chaos.State().Active {
				return 1
			}
			return 0
		}),
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			log.Error("demo-api metric unavailable", "error", err)
		}
	}

	s.handler = s.guard.Middleware(s.routes())
	return s
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/feed", s.app(25*time.Millisecond, http.StatusOK, func() any {
		return map[string]any{"items": []map[string]string{
			{"id": "item-1", "title": "Flash sale"},
			{"id": "item-2", "title": "Live match"},
		}}
	}))
	mux.HandleFunc("GET /v1/profile", s.app(10*time.Millisecond, http.StatusOK, func() any {
		return map[string]string{"user_id": "u-1", "name": "Demo User"}
	}))
	mux.HandleFunc("GET /v1/notifications", s.app(15*time.Millisecond, http.StatusOK, func() any {
		return map[string]int{"unread": 3}
	}))
	mux.HandleFunc("GET /v1/prefetch", s.app(25*time.Millisecond, http.StatusOK, func() any {
		return map[string]bool{"warmed": true}
	}))
	mux.HandleFunc("POST /v1/sync", s.app(20*time.Millisecond, http.StatusOK, func() any {
		return map[string]bool{"synced": true}
	}))
	mux.HandleFunc("POST /v1/orders", s.app(40*time.Millisecond, http.StatusCreated, func() any {
		return map[string]string{"order_id": ids.New("ord"), "status": "accepted"}
	}))

	s.guard.RegisterHandlers(mux)

	admin := func(h http.HandlerFunc) http.Handler { return httpx.RequireBearer(s.cfg.AdminToken, h) }
	mux.Handle("POST /admin/chaos", admin(s.handleSetChaos))
	mux.Handle("GET /admin/chaos", admin(s.handleGetChaos))
	mux.Handle("POST /admin/reset", admin(s.handleReset))

	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s *Server) app(serviceTime time.Duration, status int, body func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.chaos.Outage() {
			httpx.WriteError(w, http.StatusServiceUnavailable, "outage", "dependency down")
			return
		}
		if err := s.capacity.Do(r.Context(), serviceTime); err != nil {
			if errors.Is(err, ErrPoolTimeout) {
				s.poolTimeout.Inc()
				httpx.WriteError(w, http.StatusInternalServerError, "db_pool_timeout", "timed out waiting for a database connection")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "request cancelled")
			return
		}
		httpx.WriteJSON(w, status, body())
	}
}

func (s *Server) handleSetChaos(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind      string `json:"kind"`
		ExtraMs   int64  `json:"extra_ms"`
		DurationS int    `json:"duration_s"`
	}
	if err := httpx.ReadJSON(w, r, &req, maxBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	switch req.Kind {
	case ChaosNone:
		s.chaos.Set(ChaosNone, 0, 0)
	case ChaosSlow, ChaosOutage:
		if req.DurationS < 1 || req.DurationS > maxChaosDurationS {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", "duration_s must be between 1 and 600")
			return
		}
		if req.ExtraMs < 0 || req.ExtraMs > maxChaosExtraMs {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", "extra_ms must be between 0 and 10000")
			return
		}
		extra := time.Duration(req.ExtraMs) * time.Millisecond
		if req.Kind == ChaosOutage {
			extra = 0
		}
		s.chaos.Set(req.Kind, extra, time.Duration(req.DurationS)*time.Second)
		s.log.Info("chaos set", "kind", req.Kind, "extra_ms", extra.Milliseconds(), "duration_s", req.DurationS)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "kind must be slow, outage or none")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.chaos.State())
}

func (s *Server) handleGetChaos(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.chaos.State())
}

func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.guard.Reset()
	s.chaos.Set(ChaosNone, 0, 0)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Run(ctx context.Context) error {
	if s.cfg.AdminToken == DefaultAdminToken {
		s.log.Warn("using the default admin token; set ADMIN_TOKEN outside local development")
	}
	s.log.Info("demo-api starting",
		"listen", s.cfg.Listen, "enforce", s.cfg.Guard.Enforce, "db_conns", s.cfg.DBConns,
		"db_wait_timeout_ms", s.cfg.DBWaitTimeout.Milliseconds(), "db_respect_cancel", s.cfg.DBRespectCancel,
		"max_in_flight", s.cfg.Guard.MaxInFlight, "window_s", s.cfg.Guard.WindowSeconds)

	s.guard.Start(ctx)
	srv := httpx.NewServer(s.cfg.Listen, s.handler)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve %s: %w", s.cfg.Listen, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
