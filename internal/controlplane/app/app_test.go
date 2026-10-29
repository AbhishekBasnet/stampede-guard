package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	t.Logf("defaults: %+v", cfg)
	if cfg.Listen != ":8090" || cfg.DBPath != "./data/control-plane.db" || cfg.AdminToken != DefaultAdminToken ||
		cfg.TargetHealthURL != "http://localhost:8080/guard/health" || cfg.HealthPollInterval != 500*time.Millisecond ||
		cfg.HealthStaleAfter != 3*time.Second || cfg.TargetCapacityRPS != 900 || cfg.TargetUtilization != 0.8 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.PacerMinRate != 20 || cfg.PacerMaxRate != 5000 || cfg.PacerStartRate != 20 ||
		cfg.PacerAdditiveIncreasePerS != 40 || cfg.PacerDecreaseFactor != 0.5 ||
		cfg.PacerDecreaseCooldown != time.Second || cfg.PacerHighHeadroom != 0.6 {
		t.Fatalf("unexpected pacer defaults: %+v", cfg)
	}
	if cfg.DispatchTick != 250*time.Millisecond || cfg.DispatchSendConcurrency != 32 || cfg.DispatchMaxAttempts != 3 ||
		cfg.AmpDefaultPrior != 1.5 || cfg.AmpMinSent != 500 || cfg.AmpMinElapsed != 20*time.Second || cfg.AmpSettle != 30*time.Second {
		t.Fatalf("unexpected dispatch defaults: %+v", cfg)
	}
	if cfg.FCMBaseURL != "http://localhost:8070" || cfg.FCMProjectID != "demo-project" ||
		cfg.FCMCredentialsFile != "" || cfg.FCMTimeout != 5*time.Second {
		t.Fatalf("unexpected FCM defaults: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct{ name, key, val string }{
		{"not a number", "PACER_MAX_RATE", "lots"},
		{"max below min", "PACER_MAX_RATE", "10"},
		{"start below min", "PACER_START_RATE", "5"},
		{"decrease factor 1", "PACER_DECREASE_FACTOR", "1"},
		{"zero utilization", "TARGET_UTILIZATION", "0"},
		{"zero tick", "DISPATCH_TICK_MS", "0"},
		{"zero attempts", "DISPATCH_MAX_ATTEMPTS", "0"},
		{"negative capacity", "TARGET_CAPACITY_RPS", "-1"},
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

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBPath = filepath.Join(t.TempDir(), "cp.db")
	cfg.AdminToken = "test-token"
	a, err := New(context.Background(), cfg, clock.NewFake(time.Unix(1000, 0)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.Close() })
	return a
}

func do(a *App, method, path, body string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer test-token")
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandlerPublicEndpoints(t *testing.T) {
	a := newTestApp(t)
	if rec := do(a, "GET", "/healthz", "", false); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz: status %d body %q", rec.Code, rec.Body.String())
	}
	if rec := do(a, "GET", "/metrics", "", false); rec.Code != http.StatusOK {
		t.Fatalf("metrics: status %d", rec.Code)
	}
	if rec := do(a, "GET", "/v1/devices/count", "", false); rec.Code != 401 {
		t.Fatalf("count without token: status %d, want 401", rec.Code)
	}
}

func TestPhase5WalkThrough(t *testing.T) {
	a := newTestApp(t)
	rec := do(a, "POST", "/v1/devices/batch", `{"devices":[{"token":"dev-000001","platform":"android","app_build":120}]}`, true)
	if strings.TrimSpace(rec.Body.String()) != `{"upserted":1}` {
		t.Fatalf("batch body = %q", rec.Body.String())
	}
	if rec = do(a, "GET", "/v1/devices/count", "", true); strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("count body = %q", rec.Body.String())
	}
	rec = do(a, "POST", "/v1/campaigns",
		`{"name":"test","title":"Hi","body":"Hello","audience":{"kind":"all"},"pacing":"adaptive"}`, true)
	t.Logf("create: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	for _, want := range []string{`"status":"draft"`, `"min_rate":20`, `"max_rate":5000`, `"deadline_s":300`} {
		if rec.Code != 201 || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("create: status %d body %q, missing %s", rec.Code, rec.Body.String(), want)
		}
	}
}

func TestNewFailsOnUnwritableDatabasePath(t *testing.T) {
	cfg, _ := LoadConfig()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DBPath = filepath.Join(blocker, "nested", "cp.db")
	if _, err := New(context.Background(), cfg, clock.Real{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatalf("New succeeded with an unusable database path")
	}
}
