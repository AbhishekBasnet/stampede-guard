package policy

import (
	"errors"
	"math"
	"testing"
)

var allModes = []Mode{ModeNormal, ModeElevated, ModeBrownout, ModeShed}

func TestHeaderStrings(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeNormal, "v=1;mode=NORMAL;rb=0.1;bmin=200;bmax=10000;pf=1;bg=1;sm=1;ttl=300"},
		{ModeElevated, "v=1;mode=ELEVATED;rb=0.05;bmin=1000;bmax=30000;pf=1;bg=0;sm=2;ttl=120"},
		{ModeBrownout, "v=1;mode=BROWNOUT;rb=0.02;bmin=3000;bmax=60000;pf=0;bg=0;sm=4;ttl=120"},
		{ModeShed, "v=1;mode=SHED;rb=0;bmin=5000;bmax=120000;pf=0;bg=0;sm=8;ttl=120"},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			got := For(tt.mode).Header()
			t.Logf("For(%s).Header() = %q", tt.mode, got)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, m := range allModes {
		t.Run(m.String(), func(t *testing.T) {
			h := For(m).Header()
			got, err := ParseHeader(h)
			t.Logf("ParseHeader(%q) -> %+v, err=%v", h, got, err)
			if err != nil {
				t.Fatal(err)
			}
			if got != For(m) {
				t.Fatalf("got %+v, want %+v", got, For(m))
			}
		})
	}
}

func TestParseHeaderErrors(t *testing.T) {
	tests := []string{
		"mode=SHED",
		"v=2;mode=SHED",
		"v=1;mode=SHED;rb=abc",
		"v=1;pf=2",
		"v=1;mode=PANIC",
	}
	for _, h := range tests {
		t.Run(h, func(t *testing.T) {
			_, err := ParseHeader(h)
			t.Logf("ParseHeader(%q) err=%v", h, err)
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("got err %v, want ErrBadPolicy", err)
			}
		})
	}
}

func TestParseHeaderLenient(t *testing.T) {
	t.Run("unknown key and missing keys", func(t *testing.T) {
		p, err := ParseHeader("v=1;mode=SHED;future=7")
		t.Logf("-> %+v, err=%v", p, err)
		if err != nil {
			t.Fatal(err)
		}
		if p.Mode != ModeShed {
			t.Fatalf("mode = %s, want SHED", p.Mode)
		}
		if math.Abs(p.RetryBudgetRatio-0.10) > 1e-9 {
			t.Fatalf("rb = %v, want 0.10 (NORMAL default)", p.RetryBudgetRatio)
		}
	})
	t.Run("spaces and part without equals", func(t *testing.T) {
		p, err := ParseHeader(" v=1 ; mode=ELEVATED ; garbage ")
		t.Logf("-> %+v, err=%v", p, err)
		if err != nil {
			t.Fatal(err)
		}
		if p.Mode != ModeElevated {
			t.Fatalf("mode = %s, want ELEVATED", p.Mode)
		}
	})
}

func TestEffectivePriority(t *testing.T) {
	tests := []struct {
		name          string
		route, header Priority
		ok            bool
		want          Priority
	}{
		{"header lowers nothing when equal", PriorityUser, PriorityUser, true, PriorityUser},
		{"header lowers to prefetch", PriorityUser, PriorityPrefetch, true, PriorityPrefetch},
		{"header cannot raise", PriorityUser, PriorityCritical, true, PriorityUser},
		{"invalid header uses route", PriorityCritical, PriorityUser, false, PriorityCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectivePriority(tt.route, tt.header, tt.ok); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestAdmit(t *testing.T) {
	// rows: modes; columns: critical, user, prefetch, background (plan 7.6)
	matrix := map[Mode][4]bool{
		ModeNormal:   {true, true, true, true},
		ModeElevated: {true, true, true, false},
		ModeBrownout: {true, true, false, false},
		ModeShed:     {true, false, false, false},
	}
	prios := []Priority{PriorityCritical, PriorityUser, PriorityPrefetch, PriorityBackground}
	for _, m := range allModes {
		for i, p := range prios {
			if got, want := Admit(m, p), matrix[m][i]; got != want {
				t.Errorf("Admit(%s, %s) = %v, want %v", m, p, got, want)
			}
		}
	}
}

func TestUnknownModeFallsBackToShed(t *testing.T) {
	if For(Mode(99)) != For(ModeShed) {
		t.Fatal("For(99) should be the SHED policy")
	}
	if For(Mode(-1)) != For(ModeShed) {
		t.Fatal("For(-1) should be the SHED policy")
	}
	if RetryAfterBase(Mode(99)) != RetryAfterBase(ModeShed) {
		t.Fatal("RetryAfterBase(99) should be the SHED base")
	}
}

func TestValidCampaignID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"cmp_0123456789ab", true},
		{"cmp_0123456789AB", false},
		{"cmp_123", false},
		{"xyz_0123456789ab", false},
	}
	for _, tt := range tests {
		if got := ValidCampaignID(tt.id); got != tt.want {
			t.Errorf("ValidCampaignID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
