package experiment

import (
	"math"

	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
)

type Sample struct {
	TRel          float64
	OfferedRPS    float64
	AdmittedRPS   float64
	ShedRPS       float64
	ErrorRate     float64
	P99Ms         float64
	InFlight      int64
	Headroom      float64
	Mode          string
	PushRate      float64
	PushSentTotal int64
	Sim           herdsim.Second
}

type Summary struct {
	ScenarioID       string         `json:"scenario_id"`
	PeakOfferedRPS   float64        `json:"peak_offered_rps"`
	PeakAdmittedRPS  float64        `json:"peak_admitted_rps"`
	MaxP99Ms         float64        `json:"max_p99_ms"`
	SecondsP99Over1s int            `json:"seconds_p99_over_1s"`
	ClientFailures   int64          `json:"client_failures"`
	ShedResponses    int64          `json:"shed_responses"`
	Retries          int64          `json:"retries"`
	Suppressed       int64          `json:"suppressed"`
	OrdersAttempted  int64          `json:"orders_attempted"`
	OrdersOK         int64          `json:"orders_ok"`
	OrderSuccessRate float64        `json:"order_success_rate"`
	TimeTo95PctSentS float64        `json:"time_to_95pct_sent_s"`
	PushesSent       int64          `json:"pushes_sent"`
	PushesExpired    int64          `json:"pushes_expired"`
	RecoveryTimeS    float64        `json:"recovery_time_s"`
	SecondsInMode    map[string]int `json:"seconds_in_mode"`
}

const (
	NotApplicable = -1
	NotRecovered  = -2

	recoverWindow     = 5
	recoverP99Ms      = 250
	recoverErrorRate  = 0.01
	recoverShedShare  = 0.01
	slowP99Ms         = 1000
	pushSentThreshold = 0.95
)

type SummaryInput struct {
	ScenarioID    string
	Samples       []Sample
	Push          bool
	Targeted      int64
	PushesSent    int64
	PushesExpired int64
	ChaosEndS     float64
	HasChaos      bool
}

func Summarize(in SummaryInput) Summary {
	s := Summary{
		ScenarioID: in.ScenarioID, OrderSuccessRate: NotApplicable, TimeTo95PctSentS: NotApplicable,
		RecoveryTimeS: NotApplicable, SecondsInMode: map[string]int{},
		PushesSent: in.PushesSent, PushesExpired: in.PushesExpired,
	}
	for _, p := range in.Samples {
		s.PeakOfferedRPS = max(s.PeakOfferedRPS, p.OfferedRPS)
		s.PeakAdmittedRPS = max(s.PeakAdmittedRPS, p.AdmittedRPS)
		s.MaxP99Ms = max(s.MaxP99Ms, p.P99Ms)
		if p.P99Ms > slowP99Ms {
			s.SecondsP99Over1s++
		}
		s.ClientFailures += p.Sim.ServerErrors + p.Sim.Timeouts + p.Sim.NetErrors
		s.ShedResponses += p.Sim.Shed
		s.Retries += p.Sim.Retries
		s.Suppressed += p.Sim.Suppressed
		s.OrdersAttempted += p.Sim.Orders
		s.OrdersOK += p.Sim.OrdersOK
		if p.Mode != "" {
			s.SecondsInMode[p.Mode]++
		}
	}
	if s.OrdersAttempted > 0 {
		s.OrderSuccessRate = float64(s.OrdersOK) / float64(s.OrdersAttempted)
	}
	if in.Push {
		s.TimeTo95PctSentS = NotRecovered
		for _, p := range in.Samples {
			if in.Targeted > 0 && float64(p.PushSentTotal) >= pushSentThreshold*float64(in.Targeted) {
				s.TimeTo95PctSentS = p.TRel
				break
			}
		}
	}
	if in.HasChaos {
		s.RecoveryTimeS = recoveryTime(in.Samples, in.ChaosEndS)
	}
	return s
}

func healthy(p Sample) bool {
	if p.P99Ms >= recoverP99Ms || p.ErrorRate >= recoverErrorRate {
		return false
	}
	return p.OfferedRPS <= 0 || p.ShedRPS/p.OfferedRPS < recoverShedShare
}

func recoveryTime(samples []Sample, chaosEnd float64) float64 {
	for i := range samples {
		if samples[i].TRel < chaosEnd || i+recoverWindow > len(samples) {
			continue
		}
		ok := true
		for _, p := range samples[i : i+recoverWindow] {
			if !healthy(p) {
				ok = false
				break
			}
		}
		if ok {
			return math.Max(0, samples[i].TRel-chaosEnd)
		}
	}
	return NotRecovered
}
