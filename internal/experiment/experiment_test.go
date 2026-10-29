package experiment

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
)

func healthySample(t float64) Sample {
	return Sample{TRel: t, OfferedRPS: 400, AdmittedRPS: 400, P99Ms: 80, Mode: "NORMAL"}
}

func sickSample(t float64) Sample {
	return Sample{TRel: t, OfferedRPS: 1500, AdmittedRPS: 900, ShedRPS: 600, ErrorRate: 0.2, P99Ms: 1800, Mode: "BROWNOUT"}
}

func TestSummarizePeaksAndCounts(t *testing.T) {
	samples := []Sample{healthySample(0), sickSample(1), sickSample(2), healthySample(3)}
	samples[1].Sim = herdsim.Second{ServerErrors: 5, Timeouts: 3, NetErrors: 2, Shed: 40, Retries: 12, Suppressed: 4, Orders: 4, OrdersOK: 1}
	samples[2].Sim = herdsim.Second{Orders: 4, OrdersOK: 3, Retries: 8}
	s := Summarize(SummaryInput{ScenarioID: "S9", Samples: samples})
	t.Logf("%+v", s)
	if s.PeakOfferedRPS != 1500 || s.PeakAdmittedRPS != 900 || s.MaxP99Ms != 1800 || s.SecondsP99Over1s != 2 {
		t.Fatalf("peaks: %+v", s)
	}
	if s.ClientFailures != 10 || s.ShedResponses != 40 || s.Retries != 20 || s.Suppressed != 4 {
		t.Fatalf("counts: %+v", s)
	}
	if s.OrdersAttempted != 8 || s.OrdersOK != 4 || s.OrderSuccessRate != 0.5 {
		t.Fatalf("orders: %+v", s)
	}
	if s.SecondsInMode["NORMAL"] != 2 || s.SecondsInMode["BROWNOUT"] != 2 {
		t.Fatalf("modes: %v", s.SecondsInMode)
	}
	if s.TimeTo95PctSentS != NotApplicable || s.RecoveryTimeS != NotApplicable {
		t.Fatalf("n/a fields: %+v", s)
	}
}

func TestSummarizeNoOrders(t *testing.T) {
	s := Summarize(SummaryInput{ScenarioID: "S9", Samples: []Sample{healthySample(0)}})
	if s.OrderSuccessRate != NotApplicable {
		t.Fatalf("order success rate = %v, want -1", s.OrderSuccessRate)
	}
}

func chaosSamples(sickUntil float64, total int) []Sample {
	var out []Sample
	for i := 0; i < total; i++ {
		t := float64(i)
		if t < sickUntil {
			out = append(out, sickSample(t))
		} else {
			out = append(out, healthySample(t))
		}
	}
	return out
}

func TestRecoveryTime(t *testing.T) {
	flaky := chaosSamples(100, 40)
	for _, i := range []int{12, 13, 14, 15} {
		flaky[i] = healthySample(float64(i))
	}
	tests := []struct {
		name    string
		samples []Sample
		want    float64
	}{
		{"recovers after exactly 7 s", chaosSamples(17, 40), 7},
		{"never recovers", chaosSamples(100, 40), NotRecovered},
		{"four healthy seconds are not enough", flaky, NotRecovered},
		{"healthy at the end of chaos", chaosSamples(0, 40), 0},
		{"too few samples after recovery", chaosSamples(17, 20), NotRecovered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Summarize(SummaryInput{ScenarioID: "S5", Samples: tt.samples, HasChaos: true, ChaosEndS: 10})
			t.Logf("recovery = %v", s.RecoveryTimeS)
			if s.RecoveryTimeS != tt.want {
				t.Fatalf("recovery = %v, want %v", s.RecoveryTimeS, tt.want)
			}
		})
	}
}

func TestRecoveryRequiresLowShedShare(t *testing.T) {
	samples := chaosSamples(10, 30)
	for i := range samples {
		if samples[i].TRel >= 10 {
			samples[i].ShedRPS = 20
		}
	}
	if s := Summarize(SummaryInput{Samples: samples, HasChaos: true, ChaosEndS: 10}); s.RecoveryTimeS != NotRecovered {
		t.Fatalf("5%% shed share counted as recovered: %v", s.RecoveryTimeS)
	}
}

func TestTimeTo95PctSent(t *testing.T) {
	var samples []Sample
	for i := 0; i <= 10; i++ {
		p := healthySample(float64(i))
		p.PushSentTotal = int64(i * 100)
		samples = append(samples, p)
	}
	s := Summarize(SummaryInput{Samples: samples, Push: true, Targeted: 1000, PushesSent: 1000})
	if s.TimeTo95PctSentS != 10 {
		t.Fatalf("time to 95%% = %v, want 10", s.TimeTo95PctSentS)
	}
	s = Summarize(SummaryInput{Samples: samples[:5], Push: true, Targeted: 1000})
	if s.TimeTo95PctSentS != NotRecovered {
		t.Fatalf("unreached = %v, want -2", s.TimeTo95PctSentS)
	}
}

func TestCSVHeaderAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.csv")
	p := healthySample(1.5)
	p.Sim = herdsim.Second{Sent: 9, OK: 8}
	if err := WriteSamplesCSV(path, []Sample{p}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := "t_rel_s,offered_rps,admitted_rps,shed_rps,error_rate,p99_ms,in_flight,headroom,mode,push_rate,push_sent_total,sim_sent,sim_ok,sim_shed,sim_server_errors,sim_timeouts,sim_retries,sim_suppressed,opens,orders,orders_ok"
	if got := strings.Join(rows[0], ","); got != want {
		t.Fatalf("header = %s", got)
	}
	if len(rows) != 2 || len(rows[1]) != len(rows[0]) || rows[1][0] != "1.5" || rows[1][11] != "9" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestLineChartWritesPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	err := LineChart(path, "title", "x", "y", []Series{
		{Name: "a", X: []float64{0, 1, 2}, Y: []float64{1, 4, 2}},
		{Name: "b", X: []float64{0, 1, 2}, Y: []float64{2, 2, 3}},
		{Name: "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("png missing or empty: %v", err)
	}
	head := make([]byte, 8)
	f, _ := os.Open(path)
	defer f.Close()
	_, _ = f.Read(head)
	if string(head[1:4]) != "PNG" {
		t.Fatalf("not a PNG: %q", head)
	}
}

func TestLineChartRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if err := LineChart(filepath.Join(dir, "a.png"), "t", "x", "y", nil); err == nil {
		t.Fatal("chart without data succeeded")
	}
	if err := LineChart(filepath.Join(dir, "b.png"), "t", "x", "y", []Series{{Name: "a", X: []float64{1}, Y: nil}}); err == nil {
		t.Fatal("mismatched series succeeded")
	}
}

func TestScenarioCatalog(t *testing.T) {
	all := All()
	if len(all) != 7 {
		t.Fatalf("got %d scenarios, want 7", len(all))
	}
	for i, s := range all {
		if s.ID != fmt.Sprintf("S%d", i+1) || s.Hypothesis == "" || s.Warmup <= 0 {
			t.Fatalf("scenario %d: %+v", i, s)
		}
		if !s.IsPush() && s.Observe <= 0 {
			t.Fatalf("%s observes nothing", s.ID)
		}
	}
	s2, _ := ByID("s2")
	if s2.StaticRate != 300 || s2.Pacing != "static" {
		t.Fatalf("S2 = %+v", s2)
	}
	s4, _ := ByID("S4")
	if !s4.Enforce || s4.NaiveFraction != 0 {
		t.Fatalf("S4 = %+v", s4)
	}
	s7, _ := ByID("S7")
	if s7.SyncMode != "aligned" || s7.Warmup != 70*time.Second || s7.Observe != 150*time.Second {
		t.Fatalf("S7 = %+v", s7)
	}
	if got, err := Select("all"); err != nil || len(got) != 7 {
		t.Fatalf("Select(all) = %d, %v", len(got), err)
	}
	if got, err := Select("S3"); err != nil || len(got) != 1 || got[0].ID != "S3" {
		t.Fatalf("Select(S3) = %v, %v", got, err)
	}
	if _, err := Select("S9"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
}

var fakeEpoch = time.Unix(1_800_000_000, 0)

type fakeWorld struct {
	calls    []string
	enforce  atomic.Value
	campaign atomic.Int64
}

func (w *fakeWorld) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	record := func(name string, h http.HandlerFunc) {
		mux.HandleFunc(name, func(rw http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.calls = append(w.calls, name)
			h(rw, r)
		})
	}
	ok := func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNoContent) }
	record("POST /guard/enforce", func(rw http.ResponseWriter, r *http.Request) {
		var b struct{ Enabled bool }
		_ = json.NewDecoder(r.Body).Decode(&b)
		w.enforce.Store(b.Enabled)
		ok(rw, r)
	})
	record("POST /admin/reset", ok)
	record("POST /admin/chaos", ok)
	record("POST /sim/config", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte(`{}`)) })
	record("POST /sim/reset", ok)
	record("GET /sim/stats", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"devices":300,"config":{"naive_fraction":1}}`))
	})
	record("GET /guard/health", func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"mode":"NORMAL","offered_rps":400,"admitted_rps":390,"p99_ms":90,"headroom":0.8,"in_flight":3}`))
	})
	record("GET /sim/timeline", func(rw http.ResponseWriter, r *http.Request) {
		secs := []herdsim.Second{}
		for i := int64(0); i < 400; i++ {
			secs = append(secs, herdsim.Second{T: fakeEpoch.Unix() - 10 + i, Sent: 7, OK: 7, Orders: 1, OrdersOK: 1})
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"seconds": secs})
	})
	record("POST /v1/campaigns", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusCreated)
		_, _ = rw.Write([]byte(`{"id":"cmp_0123456789ab"}`))
	})
	record("POST /v1/campaigns/{id}/start", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte(`{}`)) })
	record("GET /v1/campaigns/{id}", func(rw http.ResponseWriter, _ *http.Request) {
		n := w.campaign.Add(1)
		status := "running"
		if n >= 3 {
			status = "completed"
		}
		_, _ = fmt.Fprintf(rw, `{"id":"cmp_0123456789ab","status":%q,"targeted":300,"sent":%d,"current_rate":50}`, status, min(n*100, 300))
	})
	return mux
}

func newTestRunner(t *testing.T, srvURL string) *Runner {
	t.Helper()
	r := NewRunner(NewClients(srvURL, srvURL, srvURL, "tok"), t.TempDir(), slog.New(slog.DiscardHandler))
	r.Interval, r.PushTail, r.PushMaxWatch = 5*time.Millisecond, 20*time.Millisecond, 5*time.Hour
	var ticks atomic.Int64
	r.Now = func() time.Time { return fakeEpoch.Add(time.Duration(ticks.Add(1)) * time.Second) }
	return r
}

func TestRunnerPushScenario(t *testing.T) {
	w := &fakeWorld{}
	srv := httptest.NewServer(w.handler(t))
	defer srv.Close()
	r := newTestRunner(t, srv.URL)

	sc, _ := ByID("S4")
	sc.Warmup = 5 * time.Millisecond
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("summary: %+v", res.Summary)
	if enforce, _ := w.enforce.Load().(bool); !enforce {
		t.Fatal("S4 did not turn enforcement on")
	}
	if w.calls[0] != "POST /guard/enforce" || w.calls[1] != "POST /admin/reset" {
		t.Fatalf("setup order: %v", w.calls[:4])
	}
	for _, f := range []string{"samples.csv", "summary.json", "rps.png", "latency.png", "failures.png", "push.png"} {
		if info, err := os.Stat(filepath.Join(res.Dir, f)); err != nil || info.Size() == 0 {
			t.Errorf("%s missing or empty: %v", f, err)
		}
	}
	s := res.Summary
	if s.PushesSent != 300 || s.TimeTo95PctSentS < 0 || s.PeakOfferedRPS != 400 || s.OrdersAttempted == 0 || s.OrderSuccessRate != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.RecoveryTimeS != NotApplicable {
		t.Fatalf("push scenario recovery = %v, want -1", s.RecoveryTimeS)
	}
}

func TestRunnerChaosScenarioAndReport(t *testing.T) {
	w := &fakeWorld{}
	srv := httptest.NewServer(w.handler(t))
	defer srv.Close()
	r := newTestRunner(t, srv.URL)

	sc, _ := ByID("S5")
	sc.Warmup, sc.Observe = 5*time.Millisecond, 80*time.Millisecond
	sc.Chaos = &ChaosSpec{Kind: "slow", ExtraMs: 500, Duration: 10 * time.Millisecond}
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawChaos bool
	for _, c := range w.calls {
		sawChaos = sawChaos || c == "POST /admin/chaos"
	}
	if !sawChaos {
		t.Fatalf("chaos never set: %v", w.calls)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "push.png")); err == nil {
		t.Fatal("chaos scenario wrote push.png")
	}

	if err := WriteReport(r.OutDir, ReportMeta{Date: time.Now(), APIURL: srv.URL, CPURL: srv.URL, SimURL: srv.URL,
		SimStats: json.RawMessage(`{"devices":300,"config":{"naive_fraction":1}}`)}, []Result{res}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.OutDir, "REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	report := string(data)
	for _, want := range []string{"# Stampede Guard experiment report", "## Comparison", "S5 slowdown-naive", "**Hypothesis:**", "![S5 rps](S5/rps.png)", "virtual devices: 300"} {
		if !strings.Contains(report, want) {
			t.Errorf("REPORT.md is missing %q", want)
		}
	}
}

func TestRunnerStopsOnContextCancel(t *testing.T) {
	w := &fakeWorld{}
	srv := httptest.NewServer(w.handler(t))
	defer srv.Close()
	r := newTestRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc, _ := ByID("S1")
	if _, err := r.Run(ctx, sc); err == nil {
		t.Fatal("Run on a cancelled context succeeded")
	}
}

func TestRunnerSetupFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srv.Close()
	r := newTestRunner(t, srv.URL)
	sc, _ := ByID("S1")
	if _, err := r.Run(context.Background(), sc); err == nil || !strings.Contains(err.Error(), "S1 setup") {
		t.Fatalf("err = %v, want setup failure", err)
	}
}

func TestMergeTimelineCountsEverySecondOnce(t *testing.T) {
	t0 := time.Unix(1000, 0)
	secs := []herdsim.Second{{T: 999, Sent: 100}, {T: 1001, Sent: 1}, {T: 1002, Sent: 2}, {T: 1003, Sent: 4}, {T: 1004, Sent: 8}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"seconds": secs})
	}))
	defer srv.Close()
	r := newTestRunner(t, srv.URL)
	samples := []timedSample{
		{at: time.Unix(1001, 900_000_000)},
		{at: time.Unix(1001, 950_000_000)},
		{at: time.Unix(1003, 100_000_000)},
	}
	if err := r.mergeTimeline(context.Background(), t0, samples); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, s := range samples {
		total += s.Sim.Sent
	}
	t.Logf("per-sample sent: %d %d %d", samples[0].Sim.Sent, samples[1].Sim.Sent, samples[2].Sim.Sent)
	if total != 7 || samples[0].Sim.Sent != 1 || samples[1].Sim.Sent != 0 || samples[2].Sim.Sent != 6 {
		t.Fatalf("sent per sample = %d %d %d, want 1 0 6 (warmup second 999 and not-yet-sampled 1004 excluded)",
			samples[0].Sim.Sent, samples[1].Sim.Sent, samples[2].Sim.Sent)
	}
}
