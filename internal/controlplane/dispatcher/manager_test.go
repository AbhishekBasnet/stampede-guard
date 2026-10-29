package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/health"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/pacer"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/push"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/guard"
)

var t0 = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

type fakeProvider struct {
	mu         sync.Mutex
	counts     map[string]int
	retryOnce  atomic.Bool
	retryAfter time.Duration
	permanent  func(token string) bool
}

func newFakeProvider() *fakeProvider { return &fakeProvider{counts: map[string]int{}} }

func (p *fakeProvider) Send(_ context.Context, m push.Message) push.Result {
	p.mu.Lock()
	p.counts[m.Token]++
	p.mu.Unlock()
	if p.retryAfter > 0 && p.retryOnce.CompareAndSwap(false, true) {
		return push.Result{Err: "rate limited", RetryAfter: p.retryAfter}
	}
	if p.permanent != nil && p.permanent(m.Token) {
		return push.Result{Permanent: true, Err: "unregistered"}
	}
	return push.Result{OK: true}
}

func (p *fakeProvider) maxPerToken() (total, most int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.counts {
		total += n
		most = max(most, n)
	}
	return total, most
}

type env struct {
	st   *store.Store
	clk  *clock.Fake
	prov *fakeProvider
	hp   *health.Poller
	snap atomic.Pointer[guard.Snapshot]
	cfg  Config
}

func newEnv(t *testing.T, mode string, devices int) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	devs := make([]store.Device, devices)
	for i := range devs {
		devs[i] = store.Device{Token: fmt.Sprintf("dev-%06d", i), Platform: "android", AppBuild: 1}
	}
	if _, err := st.UpsertDevices(ctx, devs, t0); err != nil {
		t.Fatalf("UpsertDevices: %v", err)
	}

	e := &env{st: st, clk: clock.NewFake(t0), prov: newFakeProvider()}
	e.snap.Store(&guard.Snapshot{Mode: mode, Headroom: 0.9, CampaignRequests: map[string]int64{}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(e.snap.Load())
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.hp = health.NewPoller(srv.URL, "tok", 20*time.Millisecond, time.Second, time.Hour, clock.Real{}, log)
	pctx, cancel := context.WithCancel(ctx)
	go e.hp.Run(pctx)
	t.Cleanup(cancel)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, stale := e.hp.Latest(); !stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health poller never fetched a snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}

	e.cfg = Config{
		TickInterval: 250 * time.Millisecond, SendConcurrency: 8, MaxAttempts: 3,
		CapacityRPS: 900, TargetUtilization: 0.8,
		Adaptive: pacer.AdaptiveConfig{
			MinRate: 20, MaxRate: 5000, StartRate: 20, AdditiveIncreasePerSec: 40,
			DecreaseFactor: 0.5, DecreaseCooldown: time.Second, HighHeadroom: 0.6,
		},
		Amplification: pacer.AmplificationConfig{DefaultPrior: 1.5, MinSent: 500, MinElapsed: 20 * time.Second},
		AmpSettle:     time.Hour,
	}
	return e
}

func (e *env) manager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(e.cfg, e.st, e.prov, e.hp, e.clk, prometheus.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Shutdown)
	return m
}

func (e *env) create(t *testing.T, id string, req campaign.CreateRequest) {
	t.Helper()
	req.Name, req.Title, req.Body = "test", "Hi", "Hello"
	req.Audience = campaign.Audience{Kind: campaign.AudienceAll}
	req.ApplyDefaults(20, 5000)
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := e.st.CreateCampaign(context.Background(), campaign.New(req, id, e.clk.Now())); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
}

func (e *env) get(t *testing.T, id string) *campaign.Campaign {
	t.Helper()
	c, err := e.st.GetCampaign(context.Background(), id)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	return c
}

func (e *env) runner(t *testing.T, m *Manager, id string) *runner {
	t.Helper()
	c, err := m.begin(context.Background(), id)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return m.newRunner(c, 1.5)
}

func (e *env) stepFor(t *testing.T, r *runner, steps int, dt time.Duration) {
	t.Helper()
	for i := 0; i < steps; i++ {
		done, err := r.step(context.Background(), e.clk.Now())
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if done {
			return
		}
		e.clk.Advance(dt)
	}
}

func reasons(t *testing.T, e *env, id string) []string {
	t.Helper()
	ticks, err := e.st.ListTicks(context.Background(), id)
	if err != nil {
		t.Fatalf("ListTicks: %v", err)
	}
	out := make([]string, len(ticks))
	for i, tk := range ticks {
		out[i] = tk.Reason
	}
	return out
}

func TestStaticPacingCompletes(t *testing.T) {
	e := newEnv(t, "NORMAL", 1000)
	m := e.manager(t)
	e.create(t, "cmp_static", campaign.CreateRequest{Pacing: campaign.PacingStatic, StaticRate: 100})
	r := e.runner(t, m, "cmp_static")
	e.stepFor(t, r, 40, 250*time.Millisecond)

	c := e.get(t, "cmp_static")
	t.Logf("status=%s targeted=%d sent=%d", c.Status, c.Targeted, c.Sent)
	if c.Status != campaign.StatusCompleted || c.Sent != 1000 || c.Targeted != 1000 {
		t.Fatalf("got status %s sent %d targeted %d, want completed 1000 1000", c.Status, c.Sent, c.Targeted)
	}
	if c.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
}

func TestAdaptiveShedSendsNothing(t *testing.T) {
	e := newEnv(t, "SHED", 200)
	m := e.manager(t)
	e.create(t, "cmp_shed", campaign.CreateRequest{Pacing: campaign.PacingAdaptive})
	r := e.runner(t, m, "cmp_shed")
	e.stepFor(t, r, 8, 250*time.Millisecond)

	c := e.get(t, "cmp_shed")
	rs := reasons(t, e, "cmp_shed")
	t.Logf("sent=%d reasons=%v", c.Sent, rs)
	if c.Sent != 0 || c.Status != campaign.StatusRunning {
		t.Fatalf("sent %d status %s, want 0 running", c.Sent, c.Status)
	}
	for _, reason := range rs {
		if reason != "paused_shed" {
			t.Fatalf("reason %q, want paused_shed", reason)
		}
	}
	if len(rs) != 8 {
		t.Fatalf("ticks = %d, want 8", len(rs))
	}
}

func TestDeadlineExpiresPending(t *testing.T) {
	e := newEnv(t, "NORMAL", 1000)
	m := e.manager(t)
	e.create(t, "cmp_deadline", campaign.CreateRequest{Pacing: campaign.PacingStatic, StaticRate: 20, DeadlineSeconds: 2})
	r := e.runner(t, m, "cmp_deadline")
	e.stepFor(t, r, 12, 250*time.Millisecond)

	c := e.get(t, "cmp_deadline")
	left, err := e.st.CountPending(context.Background(), "cmp_deadline")
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	t.Logf("status=%s sent=%d expired=%d pending=%d", c.Status, c.Sent, c.Expired, left)
	if c.Status != campaign.StatusExpired || c.Expired == 0 || left != 0 {
		t.Fatalf("status %s expired %d pending %d, want expired >0 and 0 pending", c.Status, c.Expired, left)
	}
	if c.Sent+c.Failed+c.Expired != c.Targeted {
		t.Fatalf("counters do not add up: %+v", c)
	}
}

func TestCrashRecoveryResumesWithoutDuplicates(t *testing.T) {
	e := newEnv(t, "NORMAL", 500)
	first := e.manager(t)
	e.create(t, "cmp_crash", campaign.CreateRequest{Pacing: campaign.PacingStatic, StaticRate: 100})
	r := e.runner(t, first, "cmp_crash")
	e.stepFor(t, r, 4, 250*time.Millisecond)
	mid := e.get(t, "cmp_crash")
	t.Logf("before shutdown: status=%s sent=%d", mid.Status, mid.Sent)
	if mid.Status != campaign.StatusRunning || mid.Sent == 0 || mid.Sent >= mid.Targeted {
		t.Fatalf("expected a partly sent running campaign, got %+v", mid)
	}
	first.Shutdown()

	e.cfg.TickInterval = 5 * time.Millisecond
	second := e.manager(t)
	if err := second.ResumeRunningOnBoot(context.Background()); err != nil {
		t.Fatalf("ResumeRunningOnBoot: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.get(t, "cmp_crash").Status != campaign.StatusCompleted {
		if time.Now().After(deadline) {
			t.Fatalf("campaign did not complete: %+v", e.get(t, "cmp_crash"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := e.get(t, "cmp_crash")
	total, most := e.prov.maxPerToken()
	t.Logf("sent=%d targeted=%d provider calls=%d max per token=%d", c.Sent, c.Targeted, total, most)
	if c.Sent != c.Targeted || most != 1 || total != int(c.Targeted) {
		t.Fatalf("sent %d targeted %d calls %d max-per-token %d", c.Sent, c.Targeted, total, most)
	}
}

func TestProviderBackoff(t *testing.T) {
	e := newEnv(t, "NORMAL", 1000)
	e.prov.retryAfter = 2 * time.Second
	m := e.manager(t)
	e.create(t, "cmp_backoff", campaign.CreateRequest{Pacing: campaign.PacingStatic, StaticRate: 100})
	r := e.runner(t, m, "cmp_backoff")
	e.stepFor(t, r, 1, 250*time.Millisecond)
	sentAfterFirst := e.get(t, "cmp_backoff").Sent

	e.stepFor(t, r, 7, 250*time.Millisecond)
	if got := e.get(t, "cmp_backoff").Sent; got != sentAfterFirst {
		t.Fatalf("sent grew during backoff: %d -> %d", sentAfterFirst, got)
	}
	rs := reasons(t, e, "cmp_backoff")
	t.Logf("reasons=%v", rs)
	for i := 1; i <= 7; i++ {
		if rs[i] != "provider_backoff" {
			t.Fatalf("tick %d reason %q, want provider_backoff", i, rs[i])
		}
	}

	e.stepFor(t, r, 1, 250*time.Millisecond)
	if got := e.get(t, "cmp_backoff").Sent; got <= sentAfterFirst {
		t.Fatalf("sends did not resume after backoff: %d", got)
	}
}

func TestPermanentFailuresFailAfterOneAttempt(t *testing.T) {
	e := newEnv(t, "NORMAL", 1000)
	e.prov.permanent = func(token string) bool { return strings.HasSuffix(token, "7") }
	m := e.manager(t)
	e.create(t, "cmp_perm", campaign.CreateRequest{Pacing: campaign.PacingUnlimited})
	r := e.runner(t, m, "cmp_perm")
	e.stepFor(t, r, 4, 250*time.Millisecond)

	c := e.get(t, "cmp_perm")
	_, most := e.prov.maxPerToken()
	t.Logf("status=%s sent=%d failed=%d max attempts=%d", c.Status, c.Sent, c.Failed, most)
	if c.Status != campaign.StatusCompleted || c.Failed != 100 || c.Sent != 900 || most != 1 {
		t.Fatalf("status %s sent %d failed %d max attempts %d, want completed 900 100 1", c.Status, c.Sent, c.Failed, most)
	}
}

func TestLifecycleTransitions(t *testing.T) {
	e := newEnv(t, "SHED", 50)
	m := e.manager(t)
	ctx := context.Background()
	e.create(t, "cmp_life", campaign.CreateRequest{Pacing: campaign.PacingAdaptive, DeadlineSeconds: 60})

	if _, err := m.Pause(ctx, "cmp_life"); err == nil || !isTransition(err) {
		t.Fatalf("pause of a draft: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := m.Start(ctx, "cmp_life"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Start(ctx, "cmp_life"); err == nil || !isTransition(err) {
		t.Fatalf("second start: err = %v, want ErrInvalidTransition", err)
	}
	if c, err := m.Pause(ctx, "cmp_life"); err != nil || c.Status != campaign.StatusPaused {
		t.Fatalf("Pause = %+v, %v", c, err)
	}
	if c, err := m.Resume(ctx, "cmp_life"); err != nil || c.Status != campaign.StatusRunning {
		t.Fatalf("Resume = %+v, %v", c, err)
	}
	if c, err := m.Abort(ctx, "cmp_life"); err != nil || c.Status != campaign.StatusAborted || c.FinishedAt == nil {
		t.Fatalf("Abort = %+v, %v", c, err)
	}
	if _, err := m.Resume(ctx, "cmp_life"); err == nil || !isTransition(err) {
		t.Fatalf("resume of an aborted campaign: err = %v", err)
	}
	if _, err := m.Start(ctx, "cmp_missing"); err == nil {
		t.Fatal("start of an unknown campaign returned no error")
	}
}

func TestResumePastDeadlineExpires(t *testing.T) {
	e := newEnv(t, "SHED", 50)
	m := e.manager(t)
	ctx := context.Background()
	e.create(t, "cmp_late", campaign.CreateRequest{Pacing: campaign.PacingAdaptive, DeadlineSeconds: 5})
	if _, err := m.Start(ctx, "cmp_late"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Pause(ctx, "cmp_late"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	e.clk.Advance(10 * time.Second)
	c, err := m.Resume(ctx, "cmp_late")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Logf("status=%s expired=%d", c.Status, c.Expired)
	if c.Status != campaign.StatusExpired || c.Expired != 50 {
		t.Fatalf("got status %s expired %d, want expired 50", c.Status, c.Expired)
	}
}

func TestFinalAmplificationStored(t *testing.T) {
	e := newEnv(t, "NORMAL", 100)
	e.cfg.AmpSettle = 10 * time.Millisecond
	e.snap.Store(&guard.Snapshot{Mode: "NORMAL", Headroom: 0.9, CampaignRequests: map[string]int64{"cmp_amp": 250}})
	m := e.manager(t)
	e.create(t, "cmp_amp", campaign.CreateRequest{Pacing: campaign.PacingUnlimited})
	r := e.runner(t, m, "cmp_amp")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if snap, _, _ := e.hp.Latest(); snap.CampaignRequests["cmp_amp"] == 250 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("poller did not pick up the updated snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.stepFor(t, r, 4, 250*time.Millisecond)

	for e.get(t, "cmp_amp").FinalAmplification == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("final amplification never stored: %+v", e.get(t, "cmp_amp"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.get(t, "cmp_amp").FinalAmplification; got != 2.5 {
		t.Fatalf("final amplification = %v, want 2.5", got)
	}
}

func isTransition(err error) bool { return strings.Contains(err.Error(), ErrInvalidTransition.Error()) }
