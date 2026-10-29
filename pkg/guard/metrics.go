package guard

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type metrics struct {
	requests    *prometheus.CounterVec
	responses   *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	headroom    prometheus.Gauge
	mode        prometheus.Gauge
	modeChanges *prometheus.CounterVec
}

func newMetrics() *metrics {
	return &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guard_requests_total", Help: "Requests seen by the guard.",
		}, []string{"priority", "outcome"}),
		responses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guard_responses_total", Help: "Responses of admitted requests by status class.",
		}, []string{"class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "guard_request_duration_seconds",
			Help:    "Latency of admitted requests.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"priority"}),
		headroom: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guard_headroom", Help: "Current headroom score, 0..1.",
		}),
		mode: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guard_mode", Help: "Effective mode, 0 (NORMAL) to 3 (SHED).",
		}),
		modeChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guard_mode_changes_total", Help: "Computed mode transitions.",
		}, []string{"from", "to"}),
	}
}

func (m *metrics) register(reg prometheus.Registerer, inFlight func() float64) error {
	inFlightGauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "guard_in_flight", Help: "Requests currently being served.",
	}, inFlight)
	for _, c := range []prometheus.Collector{
		m.requests, m.responses, m.duration, m.headroom, m.mode, m.modeChanges, inFlightGauge,
	} {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("register guard metrics: %w", err)
		}
	}
	return nil
}

func (m *metrics) observe(p policy.Priority, status int, d time.Duration) {
	m.requests.WithLabelValues(p.String(), "admitted").Inc()
	m.responses.WithLabelValues(statusClass(status)).Inc()
	m.duration.WithLabelValues(p.String()).Observe(d.Seconds())
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}
