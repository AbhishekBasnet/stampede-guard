package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/health"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/pacer"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/push"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

var ErrInvalidTransition = errors.New("invalid status transition")

type Config struct {
	TickInterval      time.Duration
	SendConcurrency   int
	MaxAttempts       int
	CapacityRPS       float64
	TargetUtilization float64
	Adaptive          pacer.AdaptiveConfig
	Amplification     pacer.AmplificationConfig
	AmpSettle         time.Duration
}

type metrics struct {
	sends              *prometheus.CounterVec
	rate, rateCap, amp *prometheus.GaugeVec
}

type Manager struct {
	cfg  Config
	st   *store.Store
	prov push.Provider
	hp   *health.Poller
	clk  clock.Clock
	log  *slog.Logger
	met  metrics

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	runners map[string]*runner
}

func NewManager(cfg Config, st *store.Store, prov push.Provider, hp *health.Poller, clk clock.Clock, reg prometheus.Registerer, log *slog.Logger) *Manager {
	byCampaign := []string{"campaign_id"}
	m := &Manager{
		cfg: cfg, st: st, prov: prov, hp: hp, clk: clk, log: log,
		runners: map[string]*runner{},
		met: metrics{
			sends: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "cp_push_sends_total", Help: "Push sends by result.",
			}, []string{"result"}),
			rate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "cp_pacer_rate", Help: "Send rate decided by the pacer, pushes per second.",
			}, byCampaign),
			rateCap: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "cp_pacer_rate_cap", Help: "Predictive rate cap from spare capacity, pushes per second.",
			}, byCampaign),
			amp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "cp_amplification", Help: "Estimated backend requests per push.",
			}, byCampaign),
		},
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if reg != nil {
		reg.MustRegister(m.met.sends, m.met.rate, m.met.rateCap, m.met.amp)
	}
	return m
}

func (m *Manager) prior(ctx context.Context, c *campaign.Campaign) (float64, error) {
	if c.AmplificationPrior > 0 {
		return c.AmplificationPrior, nil
	}
	v, ok, err := m.st.LatestFinalAmplification(ctx)
	if err != nil {
		return 0, err
	}
	if ok {
		return v, nil
	}
	return m.cfg.Amplification.DefaultPrior, nil
}

func (m *Manager) launch(ctx context.Context, c *campaign.Campaign) error {
	prior, err := m.prior(ctx, c)
	if err != nil {
		return err
	}
	r := m.newRunner(c, prior)
	rctx, cancel := context.WithCancel(m.ctx)
	r.cancel = cancel

	m.mu.Lock()
	if _, running := m.runners[c.ID]; running {
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.runners[c.ID] = r
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		r.run(rctx)
	}()
	return nil
}

func (m *Manager) forget(r *runner) {
	m.mu.Lock()
	if m.runners[r.c.ID] == r {
		delete(m.runners, r.c.ID)
	}
	m.mu.Unlock()
	m.met.rate.DeleteLabelValues(r.c.ID)
	m.met.rateCap.DeleteLabelValues(r.c.ID)
	m.met.amp.DeleteLabelValues(r.c.ID)
}

func (m *Manager) stop(id string) {
	m.mu.Lock()
	r := m.runners[id]
	m.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

func transitionErr(from, to campaign.Status) error {
	return fmt.Errorf("%s -> %s: %w", from, to, ErrInvalidTransition)
}

func (m *Manager) begin(ctx context.Context, id string) (*campaign.Campaign, error) {
	c, err := m.st.GetCampaign(ctx, id)
	if err != nil {
		return nil, err
	}
	if !campaign.CanTransition(c.Status, campaign.StatusRunning) {
		return nil, transitionErr(c.Status, campaign.StatusRunning)
	}
	all, err := m.st.AllDeviceTokens(ctx)
	if err != nil {
		return nil, err
	}
	tokens := make([]string, 0, len(all))
	for _, t := range all {
		if campaign.InAudience(c.Audience, t) {
			tokens = append(tokens, t)
		}
	}
	var baseline float64
	if snap, _, stale := m.hp.Latest(); !stale {
		baseline = snap.AdmittedRPS
	}
	now := m.clk.Now().UTC()
	deadline := now.Add(time.Duration(c.DeadlineSeconds) * time.Second)
	if err := m.st.MarkStarted(ctx, id, tokens, now, deadline, baseline); err != nil {
		return nil, err
	}
	return m.st.GetCampaign(ctx, id)
}

func (m *Manager) Start(ctx context.Context, id string) (*campaign.Campaign, error) {
	c, err := m.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.launch(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) halt(ctx context.Context, id string, to campaign.Status) (*campaign.Campaign, error) {
	c, err := m.st.GetCampaign(ctx, id)
	if err != nil {
		return nil, err
	}
	if !campaign.CanTransition(c.Status, to) {
		return nil, transitionErr(c.Status, to)
	}
	m.stop(id)
	c, err = m.st.GetCampaign(ctx, id)
	if err != nil {
		return nil, err
	}
	if !campaign.CanTransition(c.Status, to) {
		return nil, transitionErr(c.Status, to)
	}
	var finished *time.Time
	if to == campaign.StatusAborted {
		now := m.clk.Now().UTC()
		finished = &now
	}
	if err := m.st.SetStatus(ctx, id, to, finished); err != nil {
		return nil, err
	}
	return m.st.GetCampaign(ctx, id)
}

func (m *Manager) Pause(ctx context.Context, id string) (*campaign.Campaign, error) {
	return m.halt(ctx, id, campaign.StatusPaused)
}

func (m *Manager) Abort(ctx context.Context, id string) (*campaign.Campaign, error) {
	return m.halt(ctx, id, campaign.StatusAborted)
}

func (m *Manager) Resume(ctx context.Context, id string) (*campaign.Campaign, error) {
	c, err := m.st.GetCampaign(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != campaign.StatusPaused {
		return nil, transitionErr(c.Status, campaign.StatusRunning)
	}
	now := m.clk.Now().UTC()
	if !now.Before(*c.DeadlineAt) {
		if _, err := m.st.ExpirePending(ctx, id, now); err != nil {
			return nil, err
		}
		m.scheduleFinalAmplification(id)
		return m.st.GetCampaign(ctx, id)
	}
	if err := m.st.SetStatus(ctx, id, campaign.StatusRunning, nil); err != nil {
		return nil, err
	}
	c, err = m.st.GetCampaign(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.launch(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) ResumeRunningOnBoot(ctx context.Context) error {
	list, err := m.st.ListCampaignsByStatus(ctx, campaign.StatusRunning)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := m.launch(ctx, c); err != nil {
			return err
		}
		m.log.Info("resumed campaign", "campaign_id", c.ID)
	}
	return nil
}

func (m *Manager) Live(id string) (rate, rateCap, amplification float64) {
	m.mu.Lock()
	r := m.runners[id]
	m.mu.Unlock()
	if r == nil {
		return 0, 0, 0
	}
	return r.live()
}

func (m *Manager) Shutdown() {
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) scheduleFinalAmplification(id string) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTimer(m.cfg.AmpSettle)
		defer t.Stop()
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		ctx := context.Background()
		c, err := m.st.GetCampaign(ctx, id)
		if err != nil || c.Sent <= 0 {
			return
		}
		snap, _, _ := m.hp.Latest()
		v := float64(snap.CampaignRequests[id]) / float64(c.Sent)
		if err := m.st.SetFinalAmplification(ctx, id, v); err != nil {
			m.log.Error("store final amplification", "campaign_id", id, "error", err)
		}
	}()
}
