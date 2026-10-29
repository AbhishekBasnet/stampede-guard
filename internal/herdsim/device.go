package herdsim

import (
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
	"github.com/AbhishekBasnet/stampede-guard/pkg/retry"
)

type Device struct {
	Token    string
	Platform string
	AppBuild int

	offset float64
	naive  atomic.Bool

	mu         sync.Mutex
	pol        policy.FleetPolicy
	polExpires time.Time
	budget     *retry.Budget
}

func tokenHash(token string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return float64(h.Sum32()) / (1 << 32)
}

func NewDevice(index int) *Device {
	token := fmt.Sprintf("dev-%06d", index)
	platform := "ios"
	if index%2 == 0 {
		platform = "android"
	}
	return &Device{
		Token: token, Platform: platform, AppBuild: 100 + index%31,
		offset: tokenHash(token), budget: retry.NewBudget(10, 0.10),
	}
}

func (d *Device) isNaiveAt(fraction float64) bool { return d.offset < fraction }

func (d *Device) SetNaiveFraction(fraction float64) { d.naive.Store(d.isNaiveAt(fraction)) }

func (d *Device) Naive() bool { return d.naive.Load() }

func (d *Device) Policy(now time.Time) policy.FleetPolicy {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.polExpires.IsZero() || !now.Before(d.polExpires) {
		return policy.For(policy.ModeNormal)
	}
	return d.pol
}

func (d *Device) UpdatePolicy(header string, now time.Time) {
	p, err := policy.ParseHeader(header)
	if err != nil {
		return
	}
	d.mu.Lock()
	d.pol = p
	d.polExpires = now.Add(p.TTL)
	d.mu.Unlock()
	d.budget.SetRatio(p.RetryBudgetRatio)
}

func nextSyncTime(now time.Time, interval time.Duration, mode string, offset float64) time.Time {
	base := now.Truncate(interval)
	if mode == SyncAligned {
		return base.Add(interval)
	}
	next := base.Add(time.Duration(offset * float64(interval)))
	if !next.After(now) {
		next = next.Add(interval)
	}
	return next
}
