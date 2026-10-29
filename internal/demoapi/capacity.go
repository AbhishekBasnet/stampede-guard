package demoapi

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

var ErrPoolTimeout = errors.New("db pool timeout")

type Capacity struct {
	sem           chan struct{}
	waitTimeout   time.Duration
	respectCancel bool
	chaos         *Chaos
}

func NewCapacity(conns int, waitTimeout time.Duration, respectCancel bool, chaos *Chaos) *Capacity {
	return &Capacity{
		sem:           make(chan struct{}, conns),
		waitTimeout:   waitTimeout,
		respectCancel: respectCancel,
		chaos:         chaos,
	}
}

func (c *Capacity) InUse() int { return len(c.sem) }

// With respectCancel false, work for clients that already gave up still runs to completion:
// that wasted work is what makes overload self-sustaining.
func (c *Capacity) Do(ctx context.Context, serviceTime time.Duration) error {
	var cancelled <-chan struct{}
	if c.respectCancel {
		cancelled = ctx.Done()
	}

	wait := time.NewTimer(c.waitTimeout)
	defer wait.Stop()
	select {
	case c.sem <- struct{}{}:
	case <-wait.C:
		return ErrPoolTimeout
	case <-cancelled:
		return ctx.Err()
	}
	defer func() { <-c.sem }()

	// Extra latency is spent holding the connection, like a slow downstream dependency.
	hold := time.Duration(float64(serviceTime)*(0.8+0.4*rand.Float64())) + c.chaos.ExtraLatency()
	work := time.NewTimer(hold)
	defer work.Stop()
	select {
	case <-work.C:
		return nil
	case <-cancelled:
		return ctx.Err()
	}
}
