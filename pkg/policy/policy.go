// Package policy defines the Fleet Protocol v1: modes, priorities and the X-Fleet-Policy header.
package policy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderRequestPriority = "X-Request-Priority"
	HeaderCampaignID      = "X-Campaign-ID"
	HeaderClientKind      = "X-Client-Kind"
	HeaderFleetPolicy     = "X-Fleet-Policy"
	HeaderGuardShed       = "X-Guard-Shed"
)

type Mode int32

const (
	ModeNormal Mode = iota
	ModeElevated
	ModeBrownout
	ModeShed
)

var modeNames = [...]string{"NORMAL", "ELEVATED", "BROWNOUT", "SHED"}

func (m Mode) String() string {
	if m < 0 || int(m) >= len(modeNames) {
		return "UNKNOWN"
	}
	return modeNames[m]
}

func ParseMode(s string) (Mode, error) {
	for i, n := range modeNames {
		if n == s {
			return Mode(i), nil
		}
	}
	return ModeNormal, fmt.Errorf("unknown mode %q", s)
}

type Priority int

const (
	PriorityCritical Priority = iota
	PriorityUser
	PriorityPrefetch
	PriorityBackground
)

var priorityNames = [...]string{"critical", "user", "prefetch", "background"}

func (p Priority) String() string {
	if p < 0 || int(p) >= len(priorityNames) {
		return "user"
	}
	return priorityNames[p]
}

func ParsePriority(s string) (p Priority, ok bool) {
	for i, n := range priorityNames {
		if n == s {
			return Priority(i), true
		}
	}
	return PriorityUser, false
}

func EffectivePriority(route, header Priority, headerOK bool) Priority {
	if !headerOK {
		return route
	}
	if header > route {
		return header
	}
	return route
}

func Admit(m Mode, p Priority) bool {
	switch m {
	case ModeNormal:
		return true
	case ModeElevated:
		return p != PriorityBackground
	case ModeBrownout:
		return p == PriorityCritical || p == PriorityUser
	default:
		return p == PriorityCritical
	}
}

type FleetPolicy struct {
	Version           int
	Mode              Mode
	RetryBudgetRatio  float64
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	PrefetchAllowed   bool
	BackgroundAllowed bool
	SyncMultiplier    int
	TTL               time.Duration
}

var defaultPolicies = [4]FleetPolicy{
	{1, ModeNormal, 0.10, 200 * time.Millisecond, 10 * time.Second, true, true, 1, 300 * time.Second},
	{1, ModeElevated, 0.05, 1 * time.Second, 30 * time.Second, true, false, 2, 120 * time.Second},
	{1, ModeBrownout, 0.02, 3 * time.Second, 60 * time.Second, false, false, 4, 120 * time.Second},
	{1, ModeShed, 0.00, 5 * time.Second, 120 * time.Second, false, false, 8, 120 * time.Second},
}

var retryAfterBase = [4]time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}

// For returns the policy for mode m. Unknown modes return the SHED policy (safest).
func For(m Mode) FleetPolicy {
	if m < 0 || int(m) >= len(defaultPolicies) {
		return defaultPolicies[ModeShed]
	}
	return defaultPolicies[m]
}

func RetryAfterBase(m Mode) time.Duration {
	if m < 0 || int(m) >= len(retryAfterBase) {
		return retryAfterBase[ModeShed]
	}
	return retryAfterBase[m]
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (p FleetPolicy) Header() string {
	return fmt.Sprintf("v=%d;mode=%s;rb=%s;bmin=%d;bmax=%d;pf=%s;bg=%s;sm=%d;ttl=%d",
		p.Version, p.Mode, strconv.FormatFloat(p.RetryBudgetRatio, 'f', -1, 64),
		p.BackoffMin.Milliseconds(), p.BackoffMax.Milliseconds(),
		boolDigit(p.PrefetchAllowed), boolDigit(p.BackgroundAllowed), p.SyncMultiplier, int(p.TTL.Seconds()))
}

var ErrBadPolicy = errors.New("bad fleet policy header")

func ParseHeader(h string) (FleetPolicy, error) {
	p := For(ModeNormal)
	p.Version = 0
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		var err error
		switch k {
		case "v":
			p.Version, err = strconv.Atoi(v)
		case "mode":
			p.Mode, err = ParseMode(v)
		case "rb":
			p.RetryBudgetRatio, err = strconv.ParseFloat(v, 64)
		case "bmin", "bmax", "sm", "ttl":
			var n int
			n, err = strconv.Atoi(v)
			switch k {
			case "bmin":
				p.BackoffMin = time.Duration(n) * time.Millisecond
			case "bmax":
				p.BackoffMax = time.Duration(n) * time.Millisecond
			case "sm":
				p.SyncMultiplier = n
			case "ttl":
				p.TTL = time.Duration(n) * time.Second
			}
		case "pf", "bg":
			if v != "0" && v != "1" {
				err = errors.New("must be 0 or 1")
			}
			if k == "pf" {
				p.PrefetchAllowed = v == "1"
			} else {
				p.BackgroundAllowed = v == "1"
			}
		}
		if err != nil {
			return FleetPolicy{}, fmt.Errorf("%w: key %s: %v", ErrBadPolicy, k, err)
		}
	}
	if p.Version != 1 {
		return FleetPolicy{}, fmt.Errorf("%w: version must be 1", ErrBadPolicy)
	}
	return p, nil
}

func ValidCampaignID(s string) bool {
	if len(s) != 16 || !strings.HasPrefix(s, "cmp_") {
		return false
	}
	for _, c := range s[4:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type JSON struct {
	Version           int     `json:"version"`
	Mode              string  `json:"mode"`
	RetryBudgetRatio  float64 `json:"retry_budget_ratio"`
	BackoffMinMs      int64   `json:"backoff_min_ms"`
	BackoffMaxMs      int64   `json:"backoff_max_ms"`
	PrefetchAllowed   bool    `json:"prefetch_allowed"`
	BackgroundAllowed bool    `json:"background_allowed"`
	SyncMultiplier    int     `json:"sync_multiplier"`
	TTLSeconds        int     `json:"ttl_s"`
	Header            string  `json:"header"`
}

func (p FleetPolicy) ToJSON() JSON {
	return JSON{p.Version, p.Mode.String(), p.RetryBudgetRatio, p.BackoffMin.Milliseconds(),
		p.BackoffMax.Milliseconds(), p.PrefetchAllowed, p.BackgroundAllowed, p.SyncMultiplier,
		int(p.TTL.Seconds()), p.Header()}
}
