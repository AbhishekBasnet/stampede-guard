package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/api"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/config"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/httpx"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

const DefaultAdminToken = "dev-admin-token"

type Config struct {
	LogLevel   string
	AdminToken string
	Listen     string
	DBPath     string

	TargetHealthURL    string
	HealthPollInterval time.Duration
	HealthStaleAfter   time.Duration
	TargetCapacityRPS  float64
	TargetUtilization  float64

	PacerMinRate              int
	PacerMaxRate              int
	PacerStartRate            int
	PacerAdditiveIncreasePerS float64
	PacerDecreaseFactor       float64
	PacerDecreaseCooldown     time.Duration
	PacerHighHeadroom         float64

	DispatchTick            time.Duration
	DispatchSendConcurrency int
	DispatchMaxAttempts     int

	AmpDefaultPrior float64
	AmpMinSent      int64
	AmpMinElapsed   time.Duration
	AmpSettle       time.Duration

	FCMBaseURL         string
	FCMProjectID       string
	FCMCredentialsFile string
	FCMTimeout         time.Duration
}

func LoadConfig() (Config, error) {
	var e config.Env
	cfg := Config{
		LogLevel:   e.String("LOG_LEVEL", "info"),
		AdminToken: e.String("ADMIN_TOKEN", DefaultAdminToken),
		Listen:     e.String("CP_LISTEN", ":8090"),
		DBPath:     e.String("CP_DB_PATH", "./data/control-plane.db"),

		TargetHealthURL:    e.String("TARGET_HEALTH_URL", "http://localhost:8080/guard/health"),
		HealthPollInterval: e.Millis("HEALTH_POLL_INTERVAL_MS", 500),
		HealthStaleAfter:   e.Millis("HEALTH_STALE_AFTER_MS", 3000),
		TargetCapacityRPS:  e.Float("TARGET_CAPACITY_RPS", 900),
		TargetUtilization:  e.Float("TARGET_UTILIZATION", 0.8),

		PacerMinRate:              e.Int("PACER_MIN_RATE", 20),
		PacerMaxRate:              e.Int("PACER_MAX_RATE", 5000),
		PacerStartRate:            e.Int("PACER_START_RATE", 20),
		PacerAdditiveIncreasePerS: e.Float("PACER_ADDITIVE_INCREASE_PER_S", 40),
		PacerDecreaseFactor:       e.Float("PACER_DECREASE_FACTOR", 0.5),
		PacerDecreaseCooldown:     e.Millis("PACER_DECREASE_COOLDOWN_MS", 1000),
		PacerHighHeadroom:         e.Float("PACER_HIGH_HEADROOM", 0.6),

		DispatchTick:            e.Millis("DISPATCH_TICK_MS", 250),
		DispatchSendConcurrency: e.Int("DISPATCH_SEND_CONCURRENCY", 32),
		DispatchMaxAttempts:     e.Int("DISPATCH_MAX_ATTEMPTS", 3),

		AmpDefaultPrior: e.Float("AMP_DEFAULT_PRIOR", 1.5),
		AmpMinSent:      int64(e.Int("AMP_MIN_SENT", 500)),
		AmpMinElapsed:   e.Seconds("AMP_MIN_ELAPSED_S", 20),
		AmpSettle:       e.Seconds("AMP_SETTLE_S", 30),

		FCMBaseURL:         e.String("FCM_BASE_URL", "http://localhost:8070"),
		FCMProjectID:       e.String("FCM_PROJECT_ID", "demo-project"),
		FCMCredentialsFile: e.String("FCM_CREDENTIALS_FILE", ""),
		FCMTimeout:         e.Millis("FCM_TIMEOUT_MS", 5000),
	}
	if err := e.Err(); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

// These values feed divisions and rate clamps, so a bad one would stall or flood a campaign.
func (c Config) validate() error {
	var errs []error
	bad := func(msg string) { errs = append(errs, errors.New(msg)) }
	if c.HealthPollInterval <= 0 {
		bad("HEALTH_POLL_INTERVAL_MS must be positive")
	}
	if c.TargetUtilization <= 0 || c.TargetUtilization > 1 {
		bad("TARGET_UTILIZATION must be greater than 0 and at most 1")
	}
	if c.TargetCapacityRPS < 0 {
		bad("TARGET_CAPACITY_RPS must not be negative")
	}
	if c.PacerMinRate < 1 || c.PacerMaxRate < c.PacerMinRate {
		bad("PACER_MIN_RATE and PACER_MAX_RATE must satisfy 1 <= min <= max")
	}
	if c.PacerStartRate < c.PacerMinRate || c.PacerStartRate > c.PacerMaxRate {
		bad("PACER_START_RATE must be between PACER_MIN_RATE and PACER_MAX_RATE")
	}
	if c.PacerDecreaseFactor <= 0 || c.PacerDecreaseFactor >= 1 {
		bad("PACER_DECREASE_FACTOR must be between 0 and 1 exclusive")
	}
	if c.DispatchTick <= 0 {
		bad("DISPATCH_TICK_MS must be positive")
	}
	if c.DispatchSendConcurrency < 1 || c.DispatchMaxAttempts < 1 {
		bad("DISPATCH_SEND_CONCURRENCY and DISPATCH_MAX_ATTEMPTS must be at least 1")
	}
	return errors.Join(errs...)
}

type App struct {
	cfg      Config
	log      *slog.Logger
	store    *store.Store
	registry *prometheus.Registry
	handler  http.Handler
}

func New(ctx context.Context, cfg Config, clk clock.Clock, log *slog.Logger) (*App, error) {
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	a := &App{cfg: cfg, log: log, store: st, registry: prometheus.NewRegistry()}

	mux := http.NewServeMux()
	api.New(api.Config{
		AdminToken:     cfg.AdminToken,
		DefaultMinRate: cfg.PacerMinRate,
		DefaultMaxRate: cfg.PacerMaxRate,
	}, st, clk, log).Register(mux)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	a.handler = mux
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) Run(ctx context.Context) error {
	defer func() {
		if err := a.store.Close(); err != nil {
			a.log.Error("close store", "error", err)
		}
	}()
	if a.cfg.AdminToken == DefaultAdminToken {
		a.log.Warn("using the default admin token; set ADMIN_TOKEN outside local development")
	}
	a.log.Info("control-plane starting", "listen", a.cfg.Listen, "db_path", a.cfg.DBPath,
		"target_health_url", a.cfg.TargetHealthURL, "target_capacity_rps", a.cfg.TargetCapacityRPS,
		"target_utilization", a.cfg.TargetUtilization, "fcm_base_url", a.cfg.FCMBaseURL)

	srv := httpx.NewServer(a.cfg.Listen, a.handler)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve %s: %w", a.cfg.Listen, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
