package guard

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type Guard struct {
	cfg     Config
	hcfg    HeadroomConfig
	clock   clock.Clock
	log     *slog.Logger
	metrics *metrics
	window  *Window
	headers [4]string

	inFlight     atomic.Int64
	computedMode atomic.Int32
	overrideMode atomic.Int32 // -1 = no override
	enforce      atomic.Bool

	mu            sync.Mutex // protects machine and overrideUntil
	machine       ModeMachine
	overrideUntil time.Time

	campMu    sync.Mutex
	campaigns map[string]int64

	last atomic.Pointer[Snapshot]
}

func New(cfg Config, clk clock.Clock, reg prometheus.Registerer, log *slog.Logger) *Guard {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	g := &Guard{
		cfg:   cfg,
		clock: clk,
		log:   log,
		hcfg: HeadroomConfig{
			TargetP99Ms:  float64(cfg.TargetP99) / float64(time.Millisecond),
			MaxP99Ms:     float64(cfg.MaxP99) / float64(time.Millisecond),
			MaxErrorRate: cfg.MaxErrorRate,
			MaxInFlight:  cfg.MaxInFlight,
			MinSamples:   cfg.MinSamples,
		},
		metrics:   newMetrics(),
		window:    NewWindow(cfg.WindowSeconds),
		machine:   ModeMachine{Current: policy.ModeNormal, Since: clk.Now(), Cooldown: cfg.Cooldown},
		campaigns: make(map[string]int64),
	}
	g.overrideMode.Store(-1)
	g.enforce.Store(cfg.Enforce)
	for m := policy.ModeNormal; m <= policy.ModeShed; m++ {
		g.headers[m] = policy.For(m).Header()
	}
	err := g.metrics.register(reg, func() float64 { return float64(g.inFlight.Load()) })
	if err != nil {
		log.Error("guard metrics unavailable", "error", err)
	}
	return g
}

func (g *Guard) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(g.cfg.EvalInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.Evaluate()
			}
		}
	}()
}

func (g *Guard) Evaluate() {
	now := g.clock.Now()
	st := g.window.Stats(now)
	inFlight := g.inFlight.Load()
	scores, headroom := ComputeHeadroom(g.hcfg, st.P99, st.ErrorRate, inFlight, st.Admitted)

	g.mu.Lock()
	from := g.machine.Current
	if g.machine.Step(now, TargetMode(headroom)) {
		to := g.machine.Current
		g.log.Info("mode changed", "from", from.String(), "to", to.String(), "headroom", headroom)
		g.metrics.modeChanges.WithLabelValues(from.String(), to.String()).Inc()
	}
	computed := g.machine.Current
	g.computedMode.Store(int32(computed))
	effective := computed
	var overrideUntil *time.Time
	if om := g.overrideMode.Load(); om >= 0 {
		if now.Before(g.overrideUntil) {
			effective = policy.Mode(om)
			until := g.overrideUntil.UTC()
			overrideUntil = &until
		} else {
			g.overrideMode.Store(-1)
			g.log.Info("override expired")
		}
	}
	modeSince := g.machine.Since
	g.mu.Unlock()

	g.campMu.Lock()
	campaigns := maps.Clone(g.campaigns)
	g.campMu.Unlock()

	byPriority := make(map[string]PriorityStats, 4)
	for i := range st.AdmittedRPSBy {
		byPriority[policy.Priority(i).String()] = PriorityStats{AdmittedRPS: st.AdmittedRPSBy[i], ShedRPS: st.ShedRPSBy[i]}
	}
	snap := &Snapshot{
		At:               now.UTC(),
		Mode:             effective.String(),
		ComputedMode:     computed.String(),
		ModeSince:        modeSince.UTC(),
		OverrideActive:   overrideUntil != nil,
		OverrideUntil:    overrideUntil,
		Enforce:          g.enforce.Load(),
		Headroom:         headroom,
		Scores:           scores,
		InFlight:         inFlight,
		MaxInFlight:      g.cfg.MaxInFlight,
		WindowSeconds:    g.cfg.WindowSeconds,
		OfferedRPS:       st.AdmittedRPS + st.ShedRPS,
		AdmittedRPS:      st.AdmittedRPS,
		ShedRPS:          st.ShedRPS,
		ErrorRate:        st.ErrorRate,
		P50Ms:            st.P50,
		P95Ms:            st.P95,
		P99Ms:            st.P99,
		ByPriority:       byPriority,
		CampaignRequests: campaigns,
	}
	g.last.Store(snap)
	g.metrics.headroom.Set(headroom)
	g.metrics.mode.Set(float64(effective))
}

func (g *Guard) EffectiveMode() policy.Mode {
	if om := g.overrideMode.Load(); om >= 0 {
		return policy.Mode(om)
	}
	return policy.Mode(g.computedMode.Load())
}

func (g *Guard) Snapshot() Snapshot {
	if s := g.last.Load(); s != nil {
		return *s
	}
	g.Evaluate()
	return *g.last.Load()
}

func (g *Guard) SetOverride(m policy.Mode, ttl time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.overrideUntil = g.clock.Now().Add(ttl)
	g.overrideMode.Store(int32(m))
}

func (g *Guard) ClearOverride() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.overrideMode.Store(-1)
}

func (g *Guard) SetEnforce(on bool) { g.enforce.Store(on) }

func (g *Guard) Reset() {
	g.mu.Lock()
	g.machine = ModeMachine{Current: policy.ModeNormal, Since: g.clock.Now(), Cooldown: g.cfg.Cooldown}
	g.computedMode.Store(int32(policy.ModeNormal))
	g.overrideMode.Store(-1)
	g.mu.Unlock()

	g.window.Reset()
	g.campMu.Lock()
	clear(g.campaigns)
	g.campMu.Unlock()
	g.last.Store(nil)
}
