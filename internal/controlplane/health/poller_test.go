package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

type target struct {
	srv     *httptest.Server
	failing atomic.Bool
	auth    atomic.Value
	snap    guard.Snapshot
}

func newTarget(t *testing.T, snap guard.Snapshot) *target {
	t.Helper()
	tg := &target{snap: snap}
	tg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.auth.Store(r.Header.Get("Authorization"))
		if tg.failing.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(tg.snap)
	}))
	t.Cleanup(tg.srv.Close)
	return tg
}

func metricValue(t *testing.T, p *Poller, name string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := p.RegisterMetrics(reg); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		if m := f.GetMetric()[0]; f.GetType().String() == "COUNTER" {
			return m.GetCounter().GetValue()
		} else {
			return m.GetGauge().GetValue()
		}
	}
	t.Fatalf("%s not registered", name)
	return -1
}

func newTestPoller(tg *target, clk clock.Clock) *Poller {
	return NewPoller(tg.srv.URL, "secret", 500*time.Millisecond, 400*time.Millisecond, 3*time.Second, clk,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestLatestBeforeAnyPollIsStale(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{})
	p := newTestPoller(tg, clock.NewFake(time.Unix(1000, 0)))
	_, fetchedAt, stale := p.Latest()
	t.Logf("before any poll: stale %v fetchedAt %v", stale, fetchedAt)
	if !stale || !fetchedAt.IsZero() {
		t.Fatalf("stale %v fetchedAt %v, want true and zero", stale, fetchedAt)
	}
}

func TestPollStoresSnapshot(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{Mode: "BROWNOUT", Headroom: 0.18, P99Ms: 640, CampaignRequests: map[string]int64{"cmp_0123456789ab": 7}})
	clk := clock.NewFake(time.Unix(1000, 0))
	p := newTestPoller(tg, clk)

	p.pollOnce(context.Background())
	snap, fetchedAt, stale := p.Latest()
	t.Logf("after poll: mode %s headroom %v stale %v fetchedAt %v", snap.Mode, snap.Headroom, stale, fetchedAt)
	if stale || snap.Mode != "BROWNOUT" || snap.Headroom != 0.18 || snap.P99Ms != 640 || snap.CampaignRequests["cmp_0123456789ab"] != 7 {
		t.Fatalf("snapshot %+v stale %v", snap, stale)
	}
	if !fetchedAt.Equal(clk.Now()) {
		t.Fatalf("fetchedAt = %v, want %v", fetchedAt, clk.Now())
	}
	if got := tg.auth.Load(); got != "Bearer secret" {
		t.Fatalf("Authorization = %v, want the bearer token", got)
	}
}

func TestFailuresKeepOldSnapshotUntilStale(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{Mode: "NORMAL", Headroom: 0.9})
	clk := clock.NewFake(time.Unix(1000, 0))
	p := newTestPoller(tg, clk)
	p.pollOnce(context.Background())

	tg.failing.Store(true)
	clk.Advance(2 * time.Second)
	p.pollOnce(context.Background())
	if _, _, stale := p.Latest(); stale {
		t.Fatalf("stale after only 2 s of failures, want fresh until 3 s")
	}

	clk.Advance(2 * time.Second)
	p.pollOnce(context.Background())
	snap, _, stale := p.Latest()
	t.Logf("after 4 s of failures: stale %v, kept mode %s", stale, snap.Mode)
	if !stale || snap.Mode != "NORMAL" || snap.Headroom != 0.9 {
		t.Fatalf("stale %v snapshot %+v, want stale with the old snapshot kept", stale, snap)
	}
	if n := metricValue(t, p, "cp_health_poll_errors_total"); n != 2 {
		t.Fatalf("poll errors = %v, want 2", n)
	}

	tg.failing.Store(false)
	p.pollOnce(context.Background())
	if _, _, stale := p.Latest(); stale {
		t.Fatalf("still stale after a successful poll")
	}
}

func TestUnreachableTargetCountsAsFailure(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{})
	p := newTestPoller(tg, clock.NewFake(time.Unix(1000, 0)))
	tg.srv.Close()
	p.pollOnce(context.Background())
	if _, _, stale := p.Latest(); !stale {
		t.Fatalf("not stale with an unreachable target")
	}
	if n := metricValue(t, p, "cp_health_poll_errors_total"); n != 1 {
		t.Fatalf("poll errors = %v, want 1", n)
	}
}

func TestBadBodyCountsAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	p := NewPoller(srv.URL, "x", time.Second, time.Second, 3*time.Second, clock.NewFake(time.Unix(1000, 0)),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.pollOnce(context.Background())
	if _, _, stale := p.Latest(); !stale {
		t.Fatalf("garbage body produced a fresh snapshot")
	}
}

func TestRunPollsUntilCancelled(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{Mode: "NORMAL"})
	p := NewPoller(tg.srv.URL, "x", 10*time.Millisecond, time.Second, time.Hour, clock.Real{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, stale := p.Latest(); !stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never produced a snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRegisterMetrics(t *testing.T) {
	tg := newTarget(t, guard.Snapshot{})
	clk := clock.NewFake(time.Unix(1000, 0))
	p := newTestPoller(tg, clk)
	reg := prometheus.NewRegistry()
	if err := p.RegisterMetrics(reg); err != nil {
		t.Fatal(err)
	}
	staleGauge := func() float64 {
		families, _ := reg.Gather()
		for _, f := range families {
			if f.GetName() == "cp_health_stale" {
				return f.GetMetric()[0].GetGauge().GetValue()
			}
		}
		t.Fatal("cp_health_stale not registered")
		return -1
	}
	if staleGauge() != 1 {
		t.Fatalf("cp_health_stale = 0 before any poll, want 1")
	}
	p.pollOnce(context.Background())
	if staleGauge() != 0 {
		t.Fatalf("cp_health_stale = 1 right after a poll, want 0")
	}
	clk.Advance(4 * time.Second)
	if staleGauge() != 1 {
		t.Fatalf("cp_health_stale = 0 after 4 s, want 1")
	}
	if err := p.RegisterMetrics(reg); err == nil || !strings.Contains(err.Error(), "register health metrics") {
		t.Fatalf("registering twice: err = %v, want a wrapped registration error", err)
	}
}
