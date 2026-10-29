package herdsim

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

const (
	maxOpenDelay       = 120 * time.Second
	maxWakeUpDelay     = 1500 * time.Millisecond
	organicTick        = 100 * time.Millisecond
	registerRetryEvery = 2 * time.Second
	registerAttempts   = 30
)

func (s *Sim) handlePush(d *Device, campaignID string) {
	ctx := s.ctx
	s.stats.Add(cPushes)
	s.met.pushes.Inc()

	if s.rt.get().PrefetchOnPush {
		wake := time.Duration(s.rnd.Float64() * float64(maxWakeUpDelay))
		if s.sleep(ctx, wake) != nil {
			return
		}
		if d.Naive() || d.Policy(s.clk.Now()).PrefetchAllowed {
			s.Do(ctx, d, RequestSpec{Method: http.MethodGet, Path: "/v1/prefetch", Priority: policy.PriorityPrefetch, CampaignID: campaignID})
		} else {
			s.suppressed(policy.PriorityPrefetch)
		}
	}
	if s.rnd.Float64() < s.cfg.OpenProbability {
		delay := time.Duration(float64(s.cfg.OpenDelayMedian) * math.Exp(s.cfg.OpenDelaySigma*s.rnd.NormFloat64()))
		if s.sleep(ctx, min(delay, maxOpenDelay)) != nil {
			return
		}
		s.runSession(ctx, d, campaignID)
	}
}

func (s *Sim) suppressed(p policy.Priority) {
	s.stats.Add(cSuppressed)
	s.met.suppressed.WithLabelValues(p.String()).Inc()
}

func (s *Sim) runSession(ctx context.Context, d *Device, campaignID string) {
	s.stats.Add(cOpens)
	s.met.opens.Inc()
	for _, path := range []string{"/v1/feed", "/v1/profile", "/v1/notifications"} {
		if s.Do(ctx, d, RequestSpec{Method: http.MethodGet, Path: path, Priority: policy.PriorityUser, CampaignID: campaignID}) != OutcomeOK {
			return
		}
	}
	if s.rnd.Float64() < s.cfg.OrderProbability {
		s.stats.Add(cOrders)
		out := s.Do(ctx, d, RequestSpec{Method: http.MethodPost, Path: "/v1/orders", Priority: policy.PriorityCritical, CampaignID: campaignID})
		if out == OutcomeOK {
			s.stats.Add(cOrdersOK)
			s.met.orders.WithLabelValues("ok").Inc()
		} else {
			s.met.orders.WithLabelValues("failed").Inc()
		}
	}
}

func (s *Sim) organicLoop(ctx context.Context) {
	for {
		if s.sleep(ctx, organicTick) != nil {
			return
		}
		expected := float64(len(s.devices)) * s.cfg.OrganicOpensPerHour / 3600 * organicTick.Seconds()
		k := int(math.Floor(expected + s.rnd.Float64()))
		for range k {
			d := s.devices[s.rnd.IntN(len(s.devices))]
			go s.runSession(ctx, d, "")
		}
	}
}

func (s *Sim) syncLoop(ctx context.Context, d *Device) {
	for {
		interval := s.cfg.SyncInterval
		guarded := !d.Naive()
		if guarded {
			interval *= time.Duration(max(1, d.Policy(s.clk.Now()).SyncMultiplier))
		}
		now := s.clk.Now()
		next := nextSyncTime(now, interval, s.rt.get().SyncMode, d.offset)
		if s.sleep(ctx, next.Sub(now)) != nil {
			return
		}
		if guarded && !d.Policy(s.clk.Now()).BackgroundAllowed {
			s.suppressed(policy.PriorityBackground)
			continue
		}
		s.Do(ctx, d, RequestSpec{Method: http.MethodPost, Path: "/v1/sync", Priority: policy.PriorityBackground})
	}
}
