package guard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

const testToken = "test-token"

func newTestGuard(t *testing.T, mutate func(*Config)) (*Guard, *clock.Fake) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AdminToken = testToken
	cfg.RoutePriorities = map[string]policy.Priority{
		"POST /v1/orders":  policy.PriorityCritical,
		"GET /v1/prefetch": policy.PriorityPrefetch,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	clk := clock.NewFake(time.Unix(1000, 0))
	return New(cfg, clk, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), clk
}

func okHandler(called *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if called != nil {
			*called++
		}
		w.WriteHeader(http.StatusOK)
	})
}

func serve(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareNormalRequest(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	called := 0
	rec := serve(g.Middleware(okHandler(&called)), "GET", "/v1/feed", nil)
	t.Logf("status %d, X-Fleet-Policy %q", rec.Code, rec.Header().Get(policy.HeaderFleetPolicy))
	if rec.Code != 200 || called != 1 {
		t.Fatalf("status %d called %d, want 200 1", rec.Code, called)
	}
	if got, want := rec.Header().Get(policy.HeaderFleetPolicy), policy.For(policy.ModeNormal).Header(); got != want {
		t.Fatalf("X-Fleet-Policy = %q, want %q", got, want)
	}
}

func TestMiddlewareShedsPrefetchInBrownout(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	g.SetOverride(policy.ModeBrownout, time.Minute)
	called := 0
	h := g.Middleware(okHandler(&called))
	for i := 0; i < 50; i++ {
		rec := serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderRequestPriority: "prefetch"})
		if rec.Code != 503 || rec.Header().Get(policy.HeaderGuardShed) != "1" {
			t.Fatalf("status %d shed header %q, want 503 and 1", rec.Code, rec.Header().Get(policy.HeaderGuardShed))
		}
		secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
		if err != nil || secs < 5 || secs > 10 {
			t.Fatalf("Retry-After = %q, want an integer in [5, 10]", rec.Header().Get("Retry-After"))
		}
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "overloaded" {
			t.Fatalf("body %q: error.code = %q, err %v", rec.Body.String(), body.Error.Code, err)
		}
	}
	t.Logf("50 shed responses had Retry-After in [5, 10]")
	if called != 0 {
		t.Fatalf("handler called %d times, want 0", called)
	}
}

func TestMiddlewareCriticalSurvivesBrownout(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	g.SetOverride(policy.ModeBrownout, time.Minute)
	rec := serve(g.Middleware(okHandler(nil)), "POST", "/v1/orders", nil)
	t.Logf("critical route in BROWNOUT: status %d", rec.Code)
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
}

func TestMiddlewareClientCannotRaisePriority(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	g.SetOverride(policy.ModeBrownout, time.Minute)
	rec := serve(g.Middleware(okHandler(nil)), "GET", "/v1/prefetch", map[string]string{policy.HeaderRequestPriority: "critical"})
	t.Logf("prefetch route with critical header in BROWNOUT: status %d", rec.Code)
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestMiddlewareShadowMode(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	g.SetEnforce(false)
	g.SetOverride(policy.ModeShed, time.Minute)
	called := 0
	rec := serve(g.Middleware(okHandler(&called)), "GET", "/v1/feed", nil)
	mode := g.Snapshot().Mode
	t.Logf("shadow mode: status %d, header %q, snapshot mode %s", rec.Code, rec.Header().Get(policy.HeaderFleetPolicy), mode)
	if rec.Code != 200 || called != 1 {
		t.Fatalf("status %d called %d, want 200 1", rec.Code, called)
	}
	if got, want := rec.Header().Get(policy.HeaderFleetPolicy), policy.For(policy.ModeNormal).Header(); got != want {
		t.Fatalf("X-Fleet-Policy = %q, want the NORMAL header", got)
	}
	if mode != "SHED" {
		t.Fatalf("Snapshot().Mode = %q, want SHED", mode)
	}
}

func TestMiddlewareBulkhead(t *testing.T) {
	g, _ := newTestGuard(t, func(c *Config) { c.MaxInFlight, c.CriticalReserve = 2, 1 })
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
	}))

	var wg sync.WaitGroup
	launch := func(method, path string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serve(h, method, path, nil)
		}()
		<-started
	}
	launch("GET", "/v1/feed")
	launch("GET", "/v1/feed")

	if rec := serve(h, "GET", "/v1/feed", nil); rec.Code != 503 {
		t.Fatalf("third user request: status %d, want 503", rec.Code)
	}
	launch("POST", "/v1/orders")
	if rec := serve(h, "POST", "/v1/orders", nil); rec.Code != 503 {
		t.Fatalf("second critical request: status %d, want 503", rec.Code)
	}
	t.Logf("in flight %d of %d (+%d reserve)", g.inFlight.Load(), 2, 1)
	close(release)
	wg.Wait()
	if n := g.inFlight.Load(); n != 0 {
		t.Fatalf("in flight after release = %d, want 0", n)
	}
}

func TestMiddlewareCountsServerErrors(t *testing.T) {
	g, clk := newTestGuard(t, nil)
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	for i := 0; i < 30; i++ {
		serve(h, "GET", "/v1/feed", nil)
	}
	clk.Advance(time.Second)
	g.Evaluate()
	s := g.Snapshot()
	t.Logf("after 30 x 500: error_rate %v, computed mode %s", s.ErrorRate, s.ComputedMode)
	if !near(s.ErrorRate, 1, 1e-9) {
		t.Fatalf("ErrorRate = %v, want 1", s.ErrorRate)
	}
}

func TestMiddlewareImplicitOKIsNotAnError(t *testing.T) {
	g, clk := newTestGuard(t, nil)
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	serve(h, "GET", "/v1/feed", nil)
	clk.Advance(time.Second)
	g.Evaluate()
	if s := g.Snapshot(); s.ErrorRate != 0 || s.AdmittedRPS == 0 {
		t.Fatalf("error_rate %v admitted_rps %v, want 0 and >0", s.ErrorRate, s.AdmittedRPS)
	}
}

func TestMiddlewareExemptPaths(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	g.SetOverride(policy.ModeShed, time.Minute)
	called := 0
	rec := serve(g.Middleware(okHandler(&called)), "GET", "/healthz", map[string]string{policy.HeaderRequestPriority: "background"})
	t.Logf("/healthz in SHED: status %d, X-Fleet-Policy %q", rec.Code, rec.Header().Get(policy.HeaderFleetPolicy))
	if rec.Code != 200 || called != 1 {
		t.Fatalf("status %d called %d, want 200 1", rec.Code, called)
	}
	if v := rec.Header().Get(policy.HeaderFleetPolicy); v != "" {
		t.Fatalf("exempt path got X-Fleet-Policy %q", v)
	}
}

func TestMiddlewareCampaignCounting(t *testing.T) {
	const id = "cmp_0123456789ab"
	g, _ := newTestGuard(t, func(c *Config) { c.MaxCampaignKeys = 2 })
	h := g.Middleware(okHandler(nil))
	for i := 0; i < 3; i++ {
		serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: id})
	}
	serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: "cmp_BAD"})
	serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: "cmp_aaaaaaaaaaaa"})
	serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: "cmp_bbbbbbbbbbbb"})

	got := g.Snapshot().CampaignRequests
	t.Logf("CampaignRequests = %v", got)
	if got[id] != 3 {
		t.Errorf("CampaignRequests[%s] = %d, want 3", id, got[id])
	}
	if _, ok := got["cmp_BAD"]; ok {
		t.Errorf("invalid campaign ID was counted")
	}
	if _, ok := got["cmp_bbbbbbbbbbbb"]; ok || len(got) != 2 {
		t.Errorf("third distinct ID was counted despite MaxCampaignKeys 2: %v", got)
	}
}

func TestMiddlewareConcurrent(t *testing.T) {
	g, clk := newTestGuard(t, nil)
	h := g.Middleware(okHandler(nil))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				serve(h, "GET", "/v1/feed", map[string]string{policy.HeaderCampaignID: "cmp_0123456789ab"})
				g.Evaluate()
			}
		}()
	}
	wg.Wait()
	clk.Advance(time.Second)
	g.Evaluate()
	if got := g.Snapshot().CampaignRequests["cmp_0123456789ab"]; got != 1000 {
		t.Fatalf("campaign count = %d, want 1000", got)
	}
}
