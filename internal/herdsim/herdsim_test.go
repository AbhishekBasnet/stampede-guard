package herdsim

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

var t0 = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) fn(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return nil
}

func testConfig(apiURL string) Config {
	return Config{
		AdminToken: "tok", APIBaseURL: apiURL, Devices: 10, NaiveFraction: 1.0, SyncMode: SyncSpread,
		SyncInterval: 60 * time.Second, PrefetchOnPush: true, OpenProbability: 0, OpenDelayMedian: time.Second,
		OpenDelaySigma: 0.8, HTTPTimeout: time.Second, MaxConcurrent: 50, RegisterBatch: 4,
	}
}

func newSim(t *testing.T, cfg Config) (*Sim, *sleeps) {
	t.Helper()
	s := New(cfg, clock.NewFake(t0), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &sleeps{}
	s.sleep = rec.fn
	return s, rec
}

func shedServer(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set(policy.HeaderGuardShed, "1")
		w.Header().Set(policy.HeaderFleetPolicy, policy.For(policy.ModeBrownout).Header())
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNaiveAssignment(t *testing.T) {
	cfg := testConfig("")
	cfg.Devices, cfg.NaiveFraction = 10000, 0.25
	count := func() int {
		s := New(cfg, clock.NewFake(t0), slog.New(slog.NewTextHandler(io.Discard, nil)))
		return s.NaiveCount()
	}
	first, second := count(), count()
	t.Logf("naive devices: %d", first)
	if first < 2200 || first > 2800 {
		t.Fatalf("naive = %d, want 2200 to 2800", first)
	}
	if first != second {
		t.Fatalf("assignment not deterministic: %d then %d", first, second)
	}
}

func TestDeviceIdentity(t *testing.T) {
	d := NewDevice(1)
	e := NewDevice(2)
	if d.Token != "dev-000001" || d.Platform != "ios" || e.Platform != "android" || d.AppBuild != 101 {
		t.Fatalf("devices: %+v %+v", d, e)
	}
}

func TestNextSyncTime(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 27, h, m, s, 0, time.UTC) }
	tests := []struct {
		name   string
		now    time.Time
		mode   string
		offset float64
		want   time.Time
	}{
		{"aligned", at(10, 0, 17), SyncAligned, 0.3, at(10, 1, 0)},
		{"spread before slot", at(10, 0, 17), SyncSpread, 0.5, at(10, 0, 30)},
		{"spread after slot", at(10, 0, 45), SyncSpread, 0.5, at(10, 1, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextSyncTime(tt.now, time.Minute, tt.mode, tt.offset)
			t.Logf("%s -> %s", tt.now.Format("15:04:05"), got.Format("15:04:05"))
			if !got.Equal(tt.want) {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGuardedExecutorFollowsPolicy(t *testing.T) {
	var hits atomic.Int64
	srv := shedServer(t, &hits)
	cfg := testConfig(srv.URL)
	cfg.NaiveFraction = 0
	s, rec := newSim(t, cfg)
	d := s.devices[0]

	out := s.Do(context.Background(), d, RequestSpec{Method: "GET", Path: "/v1/feed", Priority: policy.PriorityUser})
	t.Logf("outcome=%s attempts=%d sleeps=%v", out, hits.Load(), rec.d)
	if out != OutcomeShed || hits.Load() != 4 || len(rec.d) != 3 {
		t.Fatalf("outcome %s attempts %d sleeps %d, want shed 4 3", out, hits.Load(), len(rec.d))
	}
	if got := d.Policy(t0).Mode; got != policy.ModeBrownout {
		t.Fatalf("device policy mode = %s, want BROWNOUT", got)
	}
	left := 0
	for d.budget.TryRetry() {
		left++
	}
	if left != 7 {
		t.Fatalf("retry budget left = %d, want 7 (10 minus 3 retries)", left)
	}
}

func TestGuardedBudgetExhausted(t *testing.T) {
	var hits atomic.Int64
	srv := shedServer(t, &hits)
	cfg := testConfig(srv.URL)
	cfg.NaiveFraction = 0
	s, _ := newSim(t, cfg)
	d := s.devices[0]
	for d.budget.TryRetry() {
	}
	out := s.Do(context.Background(), d, RequestSpec{Method: "GET", Path: "/v1/feed", Priority: policy.PriorityUser})
	if out != OutcomeBudgetExhausted || hits.Load() != 1 || s.stats.Total(cBudgetExhausted) != 1 {
		t.Fatalf("outcome %s attempts %d exhausted %d", out, hits.Load(), s.stats.Total(cBudgetExhausted))
	}
}

func TestNaiveExecutorIgnoresPolicy(t *testing.T) {
	var hits atomic.Int64
	srv := shedServer(t, &hits)
	s, rec := newSim(t, testConfig(srv.URL))
	d := s.devices[0]

	out := s.Do(context.Background(), d, RequestSpec{Method: "GET", Path: "/v1/feed", Priority: policy.PriorityUser})
	t.Logf("outcome=%s attempts=%d sleeps=%v", out, hits.Load(), rec.d)
	if out != OutcomeShed || hits.Load() != 6 {
		t.Fatalf("outcome %s attempts %d, want shed 6", out, hits.Load())
	}
	for _, d := range rec.d {
		if d != time.Second {
			t.Fatalf("naive delay %v, want 1s", d)
		}
	}
	if d.Policy(t0).Mode != policy.ModeNormal {
		t.Fatal("naive device adopted the fleet policy")
	}
}

func TestExecutorClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		shed   bool
		want   Outcome
		hits   int64
	}{
		{"ok", 200, false, OutcomeOK, 1},
		{"client error is not retried", 404, false, OutcomeClientError, 1},
		{"plain 503 is a server error", 503, false, OutcomeServerError, 4},
		{"429 is retryable", 429, false, OutcomeServerError, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			cfg := testConfig(srv.URL)
			cfg.NaiveFraction = 0
			s, _ := newSim(t, cfg)
			got := s.Do(context.Background(), s.devices[0], RequestSpec{Method: "GET", Path: "/x", Priority: policy.PriorityUser})
			if got != tt.want || hits.Load() != tt.hits {
				t.Fatalf("outcome %s hits %d, want %s %d", got, hits.Load(), tt.want, tt.hits)
			}
		})
	}
}

func TestRequestHeaders(t *testing.T) {
	got := make(chan http.Header, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() }))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.NaiveFraction, cfg.Devices = 0.5, 200
	s, _ := newSim(t, cfg)
	var naive, guarded *Device
	for _, d := range s.devices {
		if d.Naive() && naive == nil {
			naive = d
		}
		if !d.Naive() && guarded == nil {
			guarded = d
		}
	}
	if naive == nil || guarded == nil {
		t.Fatal("need one naive and one guarded device")
	}
	spec := RequestSpec{Method: "GET", Path: "/v1/prefetch", Priority: policy.PriorityPrefetch, CampaignID: "cmp_0123456789ab"}
	s.Do(context.Background(), naive, spec)
	h := <-got
	if h.Get(policy.HeaderClientKind) != "naive" || h.Get(policy.HeaderCampaignID) != spec.CampaignID || h.Get(policy.HeaderRequestPriority) != "" {
		t.Fatalf("naive headers: %v", h)
	}
	s.Do(context.Background(), guarded, spec)
	h = <-got
	if h.Get(policy.HeaderClientKind) != "guarded" || h.Get(policy.HeaderRequestPriority) != "prefetch" || h.Get(policy.HeaderCampaignID) != spec.CampaignID {
		t.Fatalf("guarded headers: %v", h)
	}
}

func TestGuardedPushSkipsPrefetchUnderBrownout(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.NaiveFraction = 0
	s, _ := newSim(t, cfg)
	d := s.devices[0]
	d.UpdatePolicy(policy.For(policy.ModeBrownout).Header(), t0)

	s.handlePush(d, "cmp_0123456789ab")
	if s.stats.Total(cSuppressed) != 1 || s.stats.Total(cPushes) != 1 || hits.Load() != 0 {
		t.Fatalf("suppressed %d pushes %d hits %d, want 1 1 0", s.stats.Total(cSuppressed), s.stats.Total(cPushes), hits.Load())
	}

	normal := s.devices[1]
	s.handlePush(normal, "cmp_0123456789ab")
	if hits.Load() != 1 {
		t.Fatalf("device in NORMAL mode made %d prefetch requests, want 1", hits.Load())
	}
}

func TestPushLeadsToSession(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.OpenProbability, cfg.OrderProbability = 1, 1
	s, _ := newSim(t, cfg)
	s.handlePush(s.devices[0], "cmp_0123456789ab")
	t.Logf("paths: %v", paths)
	want := []string{"GET /v1/prefetch", "GET /v1/feed", "GET /v1/profile", "GET /v1/notifications", "POST /v1/orders"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if s.stats.Total(cOpens) != 1 || s.stats.Total(cOrders) != 1 || s.stats.Total(cOrdersOK) != 1 {
		t.Fatalf("opens %d orders %d ok %d", s.stats.Total(cOpens), s.stats.Total(cOrders), s.stats.Total(cOrdersOK))
	}
}

func TestSessionStopsAtFirstFailure(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.OrderProbability = 1
	s, _ := newSim(t, cfg)
	s.runSession(context.Background(), s.devices[0], "")
	if hits.Load() != 1 || s.stats.Total(cOrders) != 0 {
		t.Fatalf("hits %d orders %d, want 1 0", hits.Load(), s.stats.Total(cOrders))
	}
}

func TestFakeFCM(t *testing.T) {
	cfg := testConfig("")
	s, _ := newSim(t, cfg)
	type push struct{ token, campaign string }
	pushed := make(chan push, 4)
	s.onPush = func(d *Device, c string) { pushed <- push{d.Token, c} }
	h := s.fcmHandler()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/projects/{project}/{action}", h)

	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	valid := `{"message":{"token":"dev-000003","notification":{"title":"Hi"},"data":{"campaign_id":"cmp_0123456789ab"}}}`

	rec := post("/v1/projects/demo/messages:send", valid)
	t.Logf("valid: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	var ok struct{ Name string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ok)
	if rec.Code != 200 || !strings.HasPrefix(ok.Name, "projects/demo/messages/") || len(ok.Name) != len("projects/demo/messages/")+12 {
		t.Fatalf("valid: %d %q", rec.Code, rec.Body.String())
	}
	select {
	case p := <-pushed:
		if p.token != "dev-000003" || p.campaign != "cmp_0123456789ab" {
			t.Fatalf("pushed %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("OnPush was not called")
	}

	tests := []struct {
		name, path, body string
		code             int
		status           string
	}{
		{"unknown token", "/v1/projects/demo/messages:send", `{"message":{"token":"nope"}}`, 404, "NOT_FOUND"},
		{"bad json", "/v1/projects/demo/messages:send", `{`, 400, "INVALID_ARGUMENT"},
		{"empty token", "/v1/projects/demo/messages:send", `{"message":{}}`, 400, "INVALID_ARGUMENT"},
		{"wrong action", "/v1/projects/demo/topics:send", valid, 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(tt.path, tt.body)
			if rec.Code != tt.code || (tt.status != "" && !strings.Contains(rec.Body.String(), tt.status)) {
				t.Fatalf("%d %q, want %d %s", rec.Code, rec.Body.String(), tt.code, tt.status)
			}
		})
	}
	select {
	case p := <-pushed:
		t.Fatalf("rejected request still pushed %+v", p)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFakeFCMRateLimit(t *testing.T) {
	cfg := testConfig("")
	cfg.FakeFCMMaxRPS = 2
	s, _ := newSim(t, cfg)
	s.onPush = func(*Device, string) {}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/projects/{project}/{action}", s.fcmHandler())
	var codes []int
	for range 3 {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/projects/demo/messages:send", strings.NewReader(`{"message":{"token":"dev-000001"}}`)))
		codes = append(codes, rec.Code)
		if rec.Code == 429 && rec.Header().Get("Retry-After") != "1" {
			t.Fatalf("429 without Retry-After: %v", rec.Header())
		}
	}
	t.Logf("codes: %v", codes)
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes = %v, want 200 200 429", codes)
	}
}

func TestStatsSwapMovesCountersIntoTimeline(t *testing.T) {
	var st Stats
	st.Add(cSent)
	st.Add(cSent)
	st.Add(cOK)
	st.Swap(t0)
	st.Add(cSent)
	st.Swap(t0.Add(time.Second))

	last := st.Last(60)
	t.Logf("timeline: %+v", last)
	if len(last) != 2 || last[0].Sent != 2 || last[0].OK != 1 || last[1].Sent != 1 || last[1].T != t0.Unix()+1 {
		t.Fatalf("timeline = %+v", last)
	}
	if tot := st.Totals(); tot.Sent != 3 || tot.OK != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	if since := st.Since(t0.Unix() + 1); len(since) != 1 {
		t.Fatalf("Since = %+v", since)
	}
	for i := 0; i < timelineSeconds+10; i++ {
		st.Swap(t0)
	}
	if got := len(st.Last(1 << 30)); got != timelineSeconds {
		t.Fatalf("timeline length = %d, want %d", got, timelineSeconds)
	}
	st.Reset()
	if len(st.Last(60)) != 0 || st.Totals().Sent != 0 {
		t.Fatal("Reset left data behind")
	}
}

func TestAdminEndpoints(t *testing.T) {
	cfg := testConfig("")
	cfg.NaiveFraction = 0
	s, _ := newSim(t, cfg)
	h := s.Handler()
	do := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer tok")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/sim/stats", "", false); rec.Code != 401 {
		t.Fatalf("stats without token: %d", rec.Code)
	}
	if rec := do("GET", "/healthz", "", false); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if rec := do("GET", "/metrics", "", false); rec.Code != 200 {
		t.Fatalf("metrics: %d", rec.Code)
	}

	rec := do("POST", "/sim/config", `{"naive_fraction":1,"sync_mode":"aligned","prefetch_on_push":false}`, true)
	t.Logf("config: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sync_mode":"aligned"`) || s.NaiveCount() != len(s.devices) {
		t.Fatalf("config: %d %q naive %d", rec.Code, rec.Body.String(), s.NaiveCount())
	}
	for _, body := range []string{`{"naive_fraction":2}`, `{"sync_mode":"random"}`, `{"nope":1}`} {
		if rec := do("POST", "/sim/config", body, true); rec.Code != 400 {
			t.Fatalf("config %s: %d, want 400", body, rec.Code)
		}
	}

	s.stats.Add(cSent)
	rec = do("GET", "/sim/stats", "", true)
	var stats struct {
		Devices      int
		NaiveDevices int `json:"naive_devices"`
		Totals       Second
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &stats)
	if stats.Devices != 10 || stats.NaiveDevices != 10 || stats.Totals.Sent != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if rec := do("GET", "/sim/timeline?since=abc", "", true); rec.Code != 400 {
		t.Fatalf("timeline bad since: %d", rec.Code)
	}
	if rec := do("GET", "/sim/timeline?since=0", "", true); rec.Code != 200 {
		t.Fatalf("timeline: %d", rec.Code)
	}
	if rec := do("POST", "/sim/reset", "", true); rec.Code != 204 || s.stats.Totals().Sent != 0 {
		t.Fatalf("reset: %d", rec.Code)
	}
}

func TestRegisterBatchesAndRetries(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	var calls atomic.Int64
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct{ Devices []struct{ Token string } }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sizes = append(sizes, len(body.Devices))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"upserted":1}`))
	}))
	defer cp.Close()
	cfg := testConfig("")
	cfg.ControlPlaneURL = cp.URL
	s, rec := newSim(t, cfg)
	if err := s.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Logf("batch sizes %v, sleeps %v", sizes, rec.d)
	if len(sizes) != 3 || sizes[0] != 4 || sizes[1] != 4 || sizes[2] != 2 {
		t.Fatalf("batch sizes = %v, want [4 4 2]", sizes)
	}
	if len(rec.d) != 1 || rec.d[0] != 2*time.Second {
		t.Fatalf("retry sleeps = %v, want one 2s wait", rec.d)
	}
}

func TestRegisterGivesUp(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer cp.Close()
	cfg := testConfig("")
	cfg.ControlPlaneURL = cp.URL
	s, rec := newSim(t, cfg)
	if err := s.Register(context.Background()); err == nil {
		t.Fatal("Register succeeded against a failing control plane")
	}
	if len(rec.d) != registerAttempts-1 {
		t.Fatalf("waited %d times, want %d", len(rec.d), registerAttempts-1)
	}
}

func TestLoadConfigDefaultsAndErrors(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Devices != 20000 || cfg.Listen != ":8070" || cfg.SyncMode != SyncSpread || cfg.SyncInterval != 60*time.Second ||
		cfg.OpenDelayMedian != 8*time.Second || cfg.MaxConcurrent != 2000 || cfg.RegisterBatch != 1000 {
		t.Fatalf("defaults: %+v", cfg)
	}
	for key, val := range map[string]string{"SIM_SYNC_MODE": "random", "SIM_NAIVE_FRACTION": "1.5", "SIM_DEVICES": "0", "SIM_REGISTER_BATCH": "9000"} {
		t.Setenv(key, val)
		if _, err := LoadConfig(); err == nil {
			t.Errorf("LoadConfig accepted %s=%s", key, val)
		}
		t.Setenv(key, "")
	}
}
