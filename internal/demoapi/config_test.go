package demoapi

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	t.Logf("defaults: listen %s token %q db_conns %d wait %v enforce %v window %ds",
		cfg.Listen, cfg.AdminToken, cfg.DBConns, cfg.DBWaitTimeout, cfg.Guard.Enforce, cfg.Guard.WindowSeconds)
	if cfg.Listen != ":8080" || cfg.AdminToken != DefaultAdminToken || cfg.DBConns != 20 ||
		cfg.DBWaitTimeout != 2*time.Second || cfg.DBRespectCancel {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	g := cfg.Guard
	if !g.Enforce || g.WindowSeconds != 5 || g.EvalInterval != 250*time.Millisecond || g.Cooldown != 10*time.Second ||
		g.TargetP99 != 250*time.Millisecond || g.MaxP99 != time.Second || g.MaxInFlight != 200 ||
		g.CriticalReserve != 20 || g.MinSamples != 20 || g.AdminToken != DefaultAdminToken {
		t.Fatalf("unexpected guard defaults: %+v", g)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("API_LISTEN", ":9999")
	t.Setenv("ADMIN_TOKEN", "secret")
	t.Setenv("GUARD_ENFORCE", "false")
	t.Setenv("GUARD_COOLDOWN_S", "3")
	t.Setenv("GUARD_MAX_ERROR_RATE", "0.1")
	t.Setenv("DB_CONNS", "5")
	t.Setenv("DB_WAIT_TIMEOUT_MS", "100")
	t.Setenv("DB_RESPECT_CANCEL", "true")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != ":9999" || cfg.AdminToken != "secret" || cfg.Guard.AdminToken != "secret" ||
		cfg.Guard.Enforce || cfg.Guard.Cooldown != 3*time.Second || !near(cfg.Guard.MaxErrorRate, 0.1) ||
		cfg.DBConns != 5 || cfg.DBWaitTimeout != 100*time.Millisecond || !cfg.DBRespectCancel {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct{ name, key, val string }{
		{"not a number", "DB_CONNS", "many"},
		{"not a bool", "GUARD_ENFORCE", "maybe"},
		{"zero connections", "DB_CONNS", "0"},
		{"zero window", "GUARD_WINDOW_S", "0"},
		{"zero error rate", "GUARD_MAX_ERROR_RATE", "0"},
		{"inverted p99", "GUARD_MAX_P99_MS", "100"},
	}
	for _, tc := range tests {
		t.Setenv(tc.key, tc.val)
		_, err := LoadConfig()
		t.Logf("%s (%s=%s): %v", tc.name, tc.key, tc.val, err)
		if err == nil {
			t.Errorf("%s: LoadConfig accepted %s=%s", tc.name, tc.key, tc.val)
		}
		t.Setenv(tc.key, "")
	}
}

func near(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
