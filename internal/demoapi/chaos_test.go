package demoapi

import (
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func TestChaosSlowExpires(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := NewChaos(clk)
	c.Set(ChaosSlow, 300*time.Millisecond, time.Second)
	t.Logf("right after Set: ExtraLatency = %v, state = %+v", c.ExtraLatency(), c.State())
	if got := c.ExtraLatency(); got != 300*time.Millisecond {
		t.Fatalf("ExtraLatency = %v, want 300ms", got)
	}
	if !c.State().Active || c.Outage() {
		t.Fatalf("state = %+v outage = %v, want active slow and no outage", c.State(), c.Outage())
	}

	clk.Advance(1001 * time.Millisecond)
	t.Logf("after 1.001 s: ExtraLatency = %v, state = %+v", c.ExtraLatency(), c.State())
	if got := c.ExtraLatency(); got != 0 {
		t.Fatalf("ExtraLatency after expiry = %v, want 0", got)
	}
	if s := c.State(); s.Active || s.Kind != ChaosNone {
		t.Fatalf("state after expiry = %+v, want inactive none", s)
	}
}

func TestChaosOutageAndNone(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := NewChaos(clk)
	c.Set(ChaosOutage, 0, time.Second)
	if !c.Outage() || c.ExtraLatency() != 0 {
		t.Fatalf("outage = %v extra = %v, want true and 0", c.Outage(), c.ExtraLatency())
	}
	c.Set(ChaosNone, 0, 0)
	if c.Outage() || c.State().Active {
		t.Fatalf("chaos still active after none: %+v", c.State())
	}
}
