package demoapi

import (
	"errors"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/platform/config"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

const DefaultAdminToken = "dev-admin-token"

type Config struct {
	LogLevel        string
	AdminToken      string
	Listen          string
	Guard           guard.Config
	DBConns         int
	DBWaitTimeout   time.Duration
	DBRespectCancel bool
}

func LoadConfig() (Config, error) {
	var e config.Env
	g := guard.DefaultConfig()
	cfg := Config{
		LogLevel:   e.String("LOG_LEVEL", "info"),
		AdminToken: e.String("ADMIN_TOKEN", DefaultAdminToken),
		Listen:     e.String("API_LISTEN", ":8080"),
	}
	g.Enforce = e.Bool("GUARD_ENFORCE", g.Enforce)
	g.WindowSeconds = e.Int("GUARD_WINDOW_S", g.WindowSeconds)
	g.EvalInterval = e.Millis("GUARD_EVAL_INTERVAL_MS", 250)
	g.Cooldown = e.Seconds("GUARD_COOLDOWN_S", 10)
	g.TargetP99 = e.Millis("GUARD_TARGET_P99_MS", 250)
	g.MaxP99 = e.Millis("GUARD_MAX_P99_MS", 1000)
	g.MaxErrorRate = e.Float("GUARD_MAX_ERROR_RATE", g.MaxErrorRate)
	g.MaxInFlight = int64(e.Int("GUARD_MAX_IN_FLIGHT", int(g.MaxInFlight)))
	g.CriticalReserve = int64(e.Int("GUARD_CRITICAL_RESERVE", int(g.CriticalReserve)))
	g.MinSamples = int64(e.Int("GUARD_MIN_SAMPLES", int(g.MinSamples)))
	g.AdminToken = cfg.AdminToken
	cfg.Guard = g
	cfg.DBConns = e.Int("DB_CONNS", 20)
	cfg.DBWaitTimeout = e.Millis("DB_WAIT_TIMEOUT_MS", 2000)
	cfg.DBRespectCancel = e.Bool("DB_RESPECT_CANCEL", false)

	if err := e.Err(); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

// Zero or inverted values here would divide by zero or never shed, so reject them at startup.
func (c Config) validate() error {
	var errs []error
	if c.DBConns < 1 {
		errs = append(errs, errors.New("DB_CONNS must be at least 1"))
	}
	if c.Guard.WindowSeconds < 1 {
		errs = append(errs, errors.New("GUARD_WINDOW_S must be at least 1"))
	}
	if c.Guard.MaxInFlight < 1 {
		errs = append(errs, errors.New("GUARD_MAX_IN_FLIGHT must be at least 1"))
	}
	if c.Guard.EvalInterval <= 0 {
		errs = append(errs, errors.New("GUARD_EVAL_INTERVAL_MS must be positive"))
	}
	if c.Guard.MaxErrorRate <= 0 {
		errs = append(errs, errors.New("GUARD_MAX_ERROR_RATE must be positive"))
	}
	if c.Guard.MaxP99 <= c.Guard.TargetP99 {
		errs = append(errs, errors.New("GUARD_MAX_P99_MS must be greater than GUARD_TARGET_P99_MS"))
	}
	return errors.Join(errs...)
}
