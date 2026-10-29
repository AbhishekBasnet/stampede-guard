package demoapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

const token = "test-token"

func newTestServer(t *testing.T, mutate func(*Config)) (*Server, *clock.Fake) {
	t.Helper()
	cfg := Config{
		LogLevel:      "error",
		AdminToken:    token,
		Listen:        ":0",
		DBConns:       20,
		DBWaitTimeout: 200 * time.Millisecond,
	}
	cfg.Guard = guard.DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	clk := clock.NewFake(time.Unix(1000, 0))
	return New(cfg, clk, slog.New(slog.NewTextHandler(io.Discard, nil))), clk
}

func do(s *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func adminHeader() map[string]string { return map[string]string{"Authorization": "Bearer " + token} }

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestRoutes(t *testing.T) {
	s, _ := newTestServer(t, nil)
	tests := []struct {
		method, path string
		status       int
		contains     string
	}{
		{"GET", "/v1/feed", 200, `"Flash sale"`},
		{"GET", "/v1/profile", 200, `"Demo User"`},
		{"GET", "/v1/notifications", 200, `"unread":3`},
		{"GET", "/v1/prefetch", 200, `"warmed":true`},
		{"POST", "/v1/sync", 200, `"synced":true`},
		{"POST", "/v1/orders", 201, `"status":"accepted"`},
		{"GET", "/healthz", 200, "ok"},
		{"GET", "/v1/policy", 200, `"mode":"NORMAL"`},
	}
	for _, tc := range tests {
		rec := do(s, tc.method, tc.path, "", nil)
		t.Logf("%s %s -> %d %s", tc.method, tc.path, rec.Code, strings.TrimSpace(rec.Body.String()))
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s %s: status %d body %q, want %d containing %q", tc.method, tc.path, rec.Code, rec.Body.String(), tc.status, tc.contains)
		}
	}
}

func TestOrderIDFormat(t *testing.T) {
	s, _ := newTestServer(t, nil)
	var body struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(do(s, "POST", "/v1/orders", "", nil).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.OrderID) != 16 || !strings.HasPrefix(body.OrderID, "ord_") {
		t.Fatalf("order_id = %q, want ord_ + 12 hex chars", body.OrderID)
	}
}

func TestFeedCarriesFleetPolicy(t *testing.T) {
	s, _ := newTestServer(t, nil)
	rec := do(s, "GET", "/v1/feed", "", nil)
	if got, want := rec.Header().Get(policy.HeaderFleetPolicy), policy.For(policy.ModeNormal).Header(); got != want {
		t.Fatalf("X-Fleet-Policy = %q, want %q", got, want)
	}
	if do(s, "GET", "/healthz", "", nil).Header().Get(policy.HeaderFleetPolicy) != "" {
		t.Fatalf("exempt /healthz carries X-Fleet-Policy")
	}
}

func TestBrownoutWalkthrough(t *testing.T) {
	s, _ := newTestServer(t, nil)

	if rec := do(s, "GET", "/guard/health", "", nil); rec.Code != 401 {
		t.Fatalf("health without token: status %d, want 401", rec.Code)
	}
	rec := do(s, "GET", "/guard/health", "", adminHeader())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mode":"NORMAL"`) {
		t.Fatalf("health: status %d body %q", rec.Code, rec.Body.String())
	}
	rec = do(s, "POST", "/guard/override", `{"mode":"BROWNOUT","ttl_s":60}`, adminHeader())
	if rec.Code != 200 {
		t.Fatalf("override: status %d body %q", rec.Code, rec.Body.String())
	}

	rec = do(s, "GET", "/v1/feed", "", map[string]string{policy.HeaderRequestPriority: "prefetch"})
	secs, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	t.Logf("prefetch-marked feed in BROWNOUT: %d, Retry-After %d, policy %s", rec.Code, secs, rec.Header().Get(policy.HeaderFleetPolicy))
	if rec.Code != 503 || rec.Header().Get(policy.HeaderGuardShed) != "1" || secs < 5 || secs > 10 {
		t.Fatalf("status %d shed %q Retry-After %d, want 503 1 in [5,10]", rec.Code, rec.Header().Get(policy.HeaderGuardShed), secs)
	}
	if !strings.Contains(rec.Header().Get(policy.HeaderFleetPolicy), "mode=BROWNOUT") {
		t.Fatalf("X-Fleet-Policy = %q, want mode=BROWNOUT", rec.Header().Get(policy.HeaderFleetPolicy))
	}

	if rec := do(s, "GET", "/v1/prefetch", "", map[string]string{policy.HeaderRequestPriority: "critical"}); rec.Code != 503 {
		t.Fatalf("raising prefetch to critical: status %d, want 503", rec.Code)
	}
	if rec := do(s, "POST", "/v1/orders", "", nil); rec.Code != 201 {
		t.Fatalf("orders in BROWNOUT: status %d, want 201", rec.Code)
	}
}

func TestOutageIsNotAShed(t *testing.T) {
	s, _ := newTestServer(t, nil)
	rec := do(s, "POST", "/admin/chaos", `{"kind":"outage","duration_s":1}`, adminHeader())
	if rec.Code != 200 {
		t.Fatalf("set chaos: status %d body %q", rec.Code, rec.Body.String())
	}
	rec = do(s, "GET", "/v1/feed", "", nil)
	t.Logf("feed during outage: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	if rec.Code != 503 || errorCode(t, rec) != "outage" {
		t.Fatalf("status %d code %q, want 503 outage", rec.Code, errorCode(t, rec))
	}
	if rec.Header().Get(policy.HeaderGuardShed) != "" {
		t.Fatalf("outage response carries X-Guard-Shed")
	}
}

func TestChaosEndpointValidation(t *testing.T) {
	s, clk := newTestServer(t, nil)
	tests := []struct {
		name, body string
		status     int
	}{
		{"unknown kind", `{"kind":"boom","duration_s":5}`, 400},
		{"zero duration", `{"kind":"slow","extra_ms":100,"duration_s":0}`, 400},
		{"duration too long", `{"kind":"slow","duration_s":601}`, 400},
		{"extra too large", `{"kind":"slow","extra_ms":10001,"duration_s":5}`, 400},
		{"negative extra", `{"kind":"slow","extra_ms":-1,"duration_s":5}`, 400},
		{"unknown field", `{"kind":"slow","duration_s":5,"foo":1}`, 400},
		{"empty body", ``, 400},
		{"valid slow", `{"kind":"slow","extra_ms":500,"duration_s":30}`, 200},
		{"none", `{"kind":"none"}`, 200},
	}
	for _, tc := range tests {
		rec := do(s, "POST", "/admin/chaos", tc.body, adminHeader())
		t.Logf("%s: %d", tc.name, rec.Code)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d (body %q)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
	}

	do(s, "POST", "/admin/chaos", `{"kind":"slow","extra_ms":500,"duration_s":30}`, adminHeader())
	rec := do(s, "GET", "/admin/chaos", "", adminHeader())
	var st ChaosState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Kind != ChaosSlow || st.ExtraMs != 500 || !st.Active || st.Until == nil || !st.Until.Equal(clk.Now().Add(30*time.Second)) {
		t.Fatalf("GET /admin/chaos = %+v", st)
	}
}

func TestAdminEndpointsNeedToken(t *testing.T) {
	s, _ := newTestServer(t, nil)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/admin/chaos"}, {"GET", "/admin/chaos"}, {"POST", "/admin/reset"},
	} {
		if rec := do(s, tc.method, tc.path, `{"kind":"none"}`, nil); rec.Code != 401 {
			t.Errorf("%s %s without token: status %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAdminReset(t *testing.T) {
	s, _ := newTestServer(t, nil)
	do(s, "POST", "/admin/chaos", `{"kind":"outage","duration_s":60}`, adminHeader())
	do(s, "POST", "/guard/override", `{"mode":"SHED"}`, adminHeader())

	if rec := do(s, "POST", "/admin/reset", "", adminHeader()); rec.Code != 204 {
		t.Fatalf("reset: status %d, want 204", rec.Code)
	}
	if s.chaos.State().Active {
		t.Fatalf("chaos still active after reset")
	}
	if rec := do(s, "GET", "/v1/feed", "", nil); rec.Code != 200 {
		t.Fatalf("feed after reset: status %d, want 200", rec.Code)
	}
}

func TestPoolTimeoutReturns500(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) {
		c.DBConns = 1
		c.DBWaitTimeout = 20 * time.Millisecond
	})
	do(s, "POST", "/admin/chaos", `{"kind":"slow","extra_ms":300,"duration_s":10}`, adminHeader())

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = do(s, "GET", "/v1/profile", "", nil).Code
		}()
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	t.Logf("two requests on a 1-connection pool: %v", codes)
	if codes[0] != 200 || codes[1] != 500 {
		t.Fatalf("codes = %v, want [200 500]", codes)
	}
	rec := do(s, "GET", "/metrics", "", nil)
	if !strings.Contains(rec.Body.String(), "demo_db_pool_timeouts_total 1") {
		t.Fatalf("metrics missing the pool timeout counter")
	}
}

func TestMetricsExposeDemoAndGuardSeries(t *testing.T) {
	s, _ := newTestServer(t, nil)
	do(s, "GET", "/v1/feed", "", nil)
	body := do(s, "GET", "/metrics", "", nil).Body.String()
	for _, name := range []string{
		"demo_db_pool_in_use", "demo_db_pool_timeouts_total", "demo_chaos_active",
		"guard_requests_total", "guard_in_flight", "guard_headroom", "guard_mode",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics is missing %s", name)
		}
	}
}
