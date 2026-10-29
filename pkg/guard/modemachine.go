package guard

import (
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

// Escalates at once but de-escalates one step at a time after Cooldown, so the mode cannot flap.
type ModeMachine struct {
	Current     policy.Mode
	Since       time.Time
	Cooldown    time.Duration
	betterSince time.Time
}

func (m *ModeMachine) Step(now time.Time, target policy.Mode) bool {
	switch {
	case target > m.Current:
		m.Current, m.Since, m.betterSince = target, now, time.Time{}
		return true
	case target == m.Current:
		m.betterSince = time.Time{}
		return false
	default:
		if m.betterSince.IsZero() {
			m.betterSince = now
			return false
		}
		if now.Sub(m.betterSince) >= m.Cooldown {
			m.Current--
			m.Since = now
			m.betterSince = now
			return true
		}
		return false
	}
}
