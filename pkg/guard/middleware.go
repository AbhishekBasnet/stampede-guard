package guard

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.isExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		routePrio, ok := g.cfg.RoutePriorities[r.Method+" "+r.URL.Path]
		if !ok {
			routePrio = policy.PriorityUser
		}
		hp, hok := policy.ParsePriority(r.Header.Get(policy.HeaderRequestPriority))
		prio := policy.EffectivePriority(routePrio, hp, hok)

		if id := r.Header.Get(policy.HeaderCampaignID); policy.ValidCampaignID(id) {
			g.countCampaign(id)
		}

		mode := g.EffectiveMode()
		enforce := g.enforce.Load()
		headerMode := mode
		if !enforce {
			headerMode = policy.ModeNormal // shadow mode never changes client behaviour
		}
		// Must be set before next writes the status line.
		w.Header().Set(policy.HeaderFleetPolicy, g.headers[headerMode])

		if enforce && !g.admit(mode, prio) {
			g.window.RecordShed(g.clock.Now(), prio)
			g.metrics.requests.WithLabelValues(prio.String(), "shed").Inc()
			writeShed(w, mode, prio)
			return
		}

		g.inFlight.Add(1)
		start := g.clock.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			g.inFlight.Add(-1)
			end := g.clock.Now()
			g.window.RecordAdmitted(end, end.Sub(start), rec.status >= 500, prio)
			g.metrics.observe(prio, rec.status, end.Sub(start))
		}()
		next.ServeHTTP(rec, r)
	})
}

func (g *Guard) admit(mode policy.Mode, prio policy.Priority) bool {
	if !policy.Admit(mode, prio) {
		return false
	}
	inFlight := g.inFlight.Load() // check-then-act: a small overshoot under races is acceptable
	if prio == policy.PriorityCritical {
		return inFlight < g.cfg.MaxInFlight+g.cfg.CriticalReserve
	}
	return inFlight < g.cfg.MaxInFlight
}

func (g *Guard) isExempt(path string) bool {
	for _, p := range g.cfg.ExemptPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (g *Guard) countCampaign(id string) {
	g.campMu.Lock()
	defer g.campMu.Unlock()
	if _, known := g.campaigns[id]; !known && len(g.campaigns) >= g.cfg.MaxCampaignKeys {
		return
	}
	g.campaigns[id]++
}

type shedBody struct {
	Error       errorBody `json:"error"`
	Mode        string    `json:"mode"`
	Priority    string    `json:"priority"`
	RetryAfterS int       `json:"retry_after_s"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeShed(w http.ResponseWriter, mode policy.Mode, prio policy.Priority) {
	base := int(policy.RetryAfterBase(mode) / time.Second)
	secs := base + rand.IntN(base+1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set(policy.HeaderGuardShed, "1")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(shedBody{
		Error:       errorBody{Code: "overloaded", Message: "request shed by guard"},
		Mode:        mode.String(),
		Priority:    prio.String(),
		RetryAfterS: secs,
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true // implicit 200
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
