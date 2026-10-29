package guard

import (
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

func TestModeMachineHysteresis(t *testing.T) {
	m := &ModeMachine{Current: policy.ModeNormal, Since: atMs(0), Cooldown: 10 * time.Second}
	tests := []struct {
		ms     int64
		target policy.Mode
		want   policy.Mode
	}{
		{0, policy.ModeShed, policy.ModeShed},
		{1000, policy.ModeNormal, policy.ModeShed},
		{10900, policy.ModeNormal, policy.ModeShed},
		{11000, policy.ModeNormal, policy.ModeBrownout},
		{15000, policy.ModeElevated, policy.ModeBrownout},
		{20900, policy.ModeNormal, policy.ModeBrownout},
		{21000, policy.ModeNormal, policy.ModeElevated},
		{25000, policy.ModeElevated, policy.ModeElevated},
		{26000, policy.ModeNormal, policy.ModeElevated},
		{35900, policy.ModeNormal, policy.ModeElevated},
		{36000, policy.ModeNormal, policy.ModeNormal},
		{37000, policy.ModeBrownout, policy.ModeBrownout},
	}
	for _, tc := range tests {
		changed := m.Step(atMs(tc.ms), tc.target)
		t.Logf("t=%dms target=%v -> current=%v changed=%v", tc.ms, tc.target, m.Current, changed)
		if m.Current != tc.want {
			t.Errorf("t=%dms target=%v: current=%v, want %v", tc.ms, tc.target, m.Current, tc.want)
		}
	}
}
