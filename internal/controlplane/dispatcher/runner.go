package dispatcher

import (
	"context"
	"sync"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/pacer"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/push"
	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/store"
	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type runner struct {
	m *Manager
	c *campaign.Campaign

	pacer  pacer.Pacer
	bucket TokenBucket
	prior  float64

	currentRate          float64
	lastTick             time.Time
	providerBackoffUntil time.Time
	sent, failed         int64
	pending              int64

	liveMu                     sync.Mutex
	liveRate, liveCap, liveAmp float64

	cancel context.CancelFunc
	done   chan struct{}
}

func (m *Manager) newRunner(c *campaign.Campaign, prior float64) *runner {
	r := &runner{
		m: m, c: c, prior: prior,
		sent: c.Sent, failed: c.Failed,
		pending: c.Targeted - c.Sent - c.Failed - c.Expired,
		done:    make(chan struct{}),
	}
	switch c.Pacing {
	case campaign.PacingUnlimited:
		r.pacer = pacer.Unlimited{}
	case campaign.PacingStatic:
		r.pacer = pacer.Static{Rate: float64(c.StaticRate)}
	default:
		cfg := m.cfg.Adaptive
		cfg.MinRate = float64(c.MinRate)
		cfg.MaxRate = float64(c.MaxRate)
		cfg.StartRate = max(cfg.MinRate, min(cfg.StartRate, cfg.MaxRate))
		r.pacer = &pacer.Adaptive{Cfg: cfg}
	}
	return r
}

func (r *runner) live() (rate, rateCap, amp float64) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	return r.liveRate, r.liveCap, r.liveAmp
}

func (r *runner) setLive(rate, rateCap, amp float64) {
	r.liveMu.Lock()
	r.liveRate, r.liveCap, r.liveAmp = rate, rateCap, amp
	r.liveMu.Unlock()
}

func (r *runner) run(ctx context.Context) {
	defer close(r.done)
	defer r.m.forget(r)
	ticker := time.NewTicker(r.m.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		done, err := r.step(context.WithoutCancel(ctx), r.m.clk.Now())
		if err != nil {
			r.m.log.Error("campaign step failed", "campaign_id", r.c.ID, "error", err)
			return
		}
		if done {
			return
		}
	}
}

func (r *runner) step(ctx context.Context, now time.Time) (bool, error) {
	m, c := r.m, r.c
	dt := now.Sub(r.lastTick).Seconds()
	if r.lastTick.IsZero() || dt <= 0 || dt > 1 {
		dt = m.cfg.TickInterval.Seconds()
	}
	r.lastTick = now

	if !now.Before(*c.DeadlineAt) {
		if _, err := m.st.ExpirePending(ctx, c.ID, now); err != nil {
			return false, err
		}
		m.scheduleFinalAmplification(c.ID)
		return true, nil
	}

	snap, _, stale := m.hp.Latest()
	mode, err := policy.ParseMode(snap.Mode)
	if err != nil {
		stale = true
	}
	amp := pacer.EstimateAmplification(m.cfg.Amplification, r.prior, r.sent, snap.CampaignRequests[c.ID], now.Sub(*c.StartedAt))
	var rateCap float64
	if c.Pacing == campaign.PacingAdaptive {
		rateCap = pacer.RateCap(m.cfg.CapacityRPS, m.cfg.TargetUtilization, c.BaselineRPS, amp)
	}
	dec := r.pacer.Next(pacer.Input{
		Now: now, TickSeconds: dt,
		Health:      pacer.Health{Stale: stale, Mode: mode, Headroom: snap.Headroom},
		CurrentRate: r.currentRate, RateCap: rateCap,
		Remaining: r.pending, TimeLeft: c.DeadlineAt.Sub(now),
	})
	r.currentRate = dec.Rate

	reason := dec.Reason
	n := 0
	if now.Before(r.providerBackoffUntil) {
		reason = "provider_backoff"
	} else {
		n = r.bucket.Take(dec.Rate, dt)
	}

	if n > 0 {
		tokens, err := m.st.FetchPending(ctx, c.ID, n)
		if err != nil {
			return false, err
		}
		results, retryAfter := r.send(ctx, tokens)
		if retryAfter > 0 {
			r.providerBackoffUntil = now.Add(retryAfter)
		}
		sentDelta, failedDelta, err := m.st.ApplyResults(ctx, c.ID, results, m.cfg.MaxAttempts, now)
		if err != nil {
			return false, err
		}
		r.sent += sentDelta
		r.failed += failedDelta
		r.pending -= sentDelta + failedDelta
		r.bucket.Spend(len(tokens))
	}

	if err := m.st.InsertTick(ctx, store.Tick{
		CampaignID: c.ID, At: now, Rate: dec.Rate, RateCap: rateCap, Reason: reason, SentTotal: r.sent,
		Headroom: snap.Headroom, Mode: snap.Mode, P99Ms: snap.P99Ms, Amplification: amp,
	}); err != nil {
		return false, err
	}
	r.setLive(dec.Rate, rateCap, amp)
	m.met.rate.WithLabelValues(c.ID).Set(dec.Rate)
	m.met.rateCap.WithLabelValues(c.ID).Set(rateCap)
	m.met.amp.WithLabelValues(c.ID).Set(amp)

	if r.pending <= 0 {
		left, err := m.st.CountPending(ctx, c.ID)
		if err != nil {
			return false, err
		}
		if left == 0 {
			if err := m.st.SetStatus(ctx, c.ID, campaign.StatusCompleted, &now); err != nil {
				return false, err
			}
			m.scheduleFinalAmplification(c.ID)
			return true, nil
		}
		r.pending = left
	}
	return false, nil
}

func (r *runner) send(ctx context.Context, tokens []string) ([]store.DeliveryResult, time.Duration) {
	results := make([]store.DeliveryResult, len(tokens))
	retryAfters := make([]time.Duration, len(tokens))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(r.m.cfg.SendConcurrency, len(tokens)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				res := r.m.prov.Send(ctx, push.Message{
					Token: tokens[i], Title: r.c.Title, Body: r.c.Body,
					CampaignID: r.c.ID, ExpiresAt: *r.c.DeadlineAt,
				})
				results[i] = store.DeliveryResult{Token: tokens[i], OK: res.OK, Permanent: res.Permanent, Err: res.Err}
				retryAfters[i] = res.RetryAfter
				switch {
				case res.OK:
					r.m.met.sends.WithLabelValues("ok").Inc()
				case res.Permanent:
					r.m.met.sends.WithLabelValues("permanent").Inc()
				default:
					r.m.met.sends.WithLabelValues("transient").Inc()
				}
			}
		}()
	}
	for i := range tokens {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var longest time.Duration
	for _, d := range retryAfters {
		longest = max(longest, d)
	}
	return results, longest
}
