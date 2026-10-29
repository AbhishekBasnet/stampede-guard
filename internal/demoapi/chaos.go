package demoapi

import (
	"sync"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

const (
	ChaosSlow   = "slow"
	ChaosOutage = "outage"
	ChaosNone   = "none"
)

type ChaosState struct {
	Kind    string     `json:"kind"`
	ExtraMs int64      `json:"extra_ms"`
	Active  bool       `json:"active"`
	Until   *time.Time `json:"until,omitempty"`
}

type Chaos struct {
	mu    sync.Mutex
	clock clock.Clock
	kind  string
	extra time.Duration
	until time.Time
}

func NewChaos(clk clock.Clock) *Chaos { return &Chaos{clock: clk, kind: ChaosNone} }

func (c *Chaos) Set(kind string, extra, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == ChaosNone {
		c.kind, c.extra, c.until = ChaosNone, 0, time.Time{}
		return
	}
	c.kind, c.extra, c.until = kind, extra, c.clock.Now().Add(d)
}

func (c *Chaos) active(kind string) bool {
	return c.kind == kind && c.clock.Now().Before(c.until)
}

func (c *Chaos) ExtraLatency() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active(ChaosSlow) {
		return c.extra
	}
	return 0
}

func (c *Chaos) Outage() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active(ChaosOutage)
}

func (c *Chaos) State() ChaosState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active(c.kind) {
		return ChaosState{Kind: ChaosNone}
	}
	until := c.until.UTC()
	return ChaosState{Kind: c.kind, ExtraMs: c.extra.Milliseconds(), Active: true, Until: &until}
}
