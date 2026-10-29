package herdsim

import (
	"errors"
	"sync"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/platform/config"
)

const (
	SyncSpread  = "spread"
	SyncAligned = "aligned"

	DefaultAdminToken = "dev-admin-token"
)

type Config struct {
	LogLevel   string
	AdminToken string
	Listen     string

	APIBaseURL      string
	ControlPlaneURL string

	Devices        int
	NaiveFraction  float64
	SyncMode       string
	SyncInterval   time.Duration
	PrefetchOnPush bool

	OpenProbability     float64
	OpenDelayMedian     time.Duration
	OpenDelaySigma      float64
	OrderProbability    float64
	OrganicOpensPerHour float64

	HTTPTimeout   time.Duration
	MaxConcurrent int

	FakeFCMLatency time.Duration
	FakeFCMMaxRPS  int
	RegisterBatch  int
}

func LoadConfig() (Config, error) {
	var e config.Env
	cfg := Config{
		LogLevel:   e.String("LOG_LEVEL", "info"),
		AdminToken: e.String("ADMIN_TOKEN", DefaultAdminToken),
		Listen:     e.String("SIM_LISTEN", ":8070"),

		APIBaseURL:      e.String("API_BASE_URL", "http://localhost:8080"),
		ControlPlaneURL: e.String("CONTROL_PLANE_URL", "http://localhost:8090"),

		Devices:        e.Int("SIM_DEVICES", 20000),
		NaiveFraction:  e.Float("SIM_NAIVE_FRACTION", 1.0),
		SyncMode:       e.String("SIM_SYNC_MODE", SyncSpread),
		SyncInterval:   e.Seconds("SIM_SYNC_INTERVAL_S", 60),
		PrefetchOnPush: e.Bool("SIM_PREFETCH_ON_PUSH", true),

		OpenProbability:     e.Float("SIM_OPEN_PROBABILITY", 0.30),
		OpenDelayMedian:     e.Millis("SIM_OPEN_DELAY_MEDIAN_MS", 8000),
		OpenDelaySigma:      e.Float("SIM_OPEN_DELAY_SIGMA", 0.8),
		OrderProbability:    e.Float("SIM_ORDER_PROBABILITY", 0.05),
		OrganicOpensPerHour: e.Float("SIM_ORGANIC_OPENS_PER_HOUR", 2),

		HTTPTimeout:   e.Millis("SIM_HTTP_TIMEOUT_MS", 3000),
		MaxConcurrent: e.Int("SIM_MAX_CONCURRENT_REQUESTS", 2000),

		FakeFCMLatency: e.Millis("SIM_FAKE_FCM_LATENCY_MS", 2),
		FakeFCMMaxRPS:  e.Int("SIM_FAKE_FCM_MAX_RPS", 0),
		RegisterBatch:  e.Int("SIM_REGISTER_BATCH", 1000),
	}
	if err := e.Err(); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var errs []error
	bad := func(msg string) { errs = append(errs, errors.New(msg)) }
	if c.Devices < 1 {
		bad("SIM_DEVICES must be at least 1")
	}
	if c.NaiveFraction < 0 || c.NaiveFraction > 1 {
		bad("SIM_NAIVE_FRACTION must be between 0 and 1")
	}
	if c.SyncMode != SyncSpread && c.SyncMode != SyncAligned {
		bad("SIM_SYNC_MODE must be spread or aligned")
	}
	if c.SyncInterval <= 0 {
		bad("SIM_SYNC_INTERVAL_S must be positive")
	}
	if c.OpenProbability < 0 || c.OpenProbability > 1 || c.OrderProbability < 0 || c.OrderProbability > 1 {
		bad("SIM_OPEN_PROBABILITY and SIM_ORDER_PROBABILITY must be between 0 and 1")
	}
	if c.HTTPTimeout <= 0 || c.MaxConcurrent < 1 || c.RegisterBatch < 1 || c.RegisterBatch > 5000 {
		bad("SIM_HTTP_TIMEOUT_MS, SIM_MAX_CONCURRENT_REQUESTS must be positive and SIM_REGISTER_BATCH between 1 and 5000")
	}
	return errors.Join(errs...)
}

type RuntimeConfig struct {
	NaiveFraction  float64 `json:"naive_fraction"`
	SyncMode       string  `json:"sync_mode"`
	PrefetchOnPush bool    `json:"prefetch_on_push"`
}

type RuntimePatch struct {
	NaiveFraction  *float64 `json:"naive_fraction"`
	SyncMode       *string  `json:"sync_mode"`
	PrefetchOnPush *bool    `json:"prefetch_on_push"`
}

func (p RuntimePatch) validate() error {
	if p.NaiveFraction != nil && (*p.NaiveFraction < 0 || *p.NaiveFraction > 1) {
		return errors.New("naive_fraction must be between 0 and 1")
	}
	if p.SyncMode != nil && *p.SyncMode != SyncSpread && *p.SyncMode != SyncAligned {
		return errors.New("sync_mode must be spread or aligned")
	}
	return nil
}

type runtimeConfig struct {
	mu  sync.RWMutex
	cfg RuntimeConfig
}

func (r *runtimeConfig) get() RuntimeConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *runtimeConfig) apply(p RuntimePatch) RuntimeConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.NaiveFraction != nil {
		r.cfg.NaiveFraction = *p.NaiveFraction
	}
	if p.SyncMode != nil {
		r.cfg.SyncMode = *p.SyncMode
	}
	if p.PrefetchOnPush != nil {
		r.cfg.PrefetchOnPush = *p.PrefetchOnPush
	}
	return r.cfg
}
