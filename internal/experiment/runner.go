package experiment

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/campaign"
	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
)

var CSVHeader = []string{
	"t_rel_s", "offered_rps", "admitted_rps", "shed_rps", "error_rate", "p99_ms", "in_flight", "headroom", "mode",
	"push_rate", "push_sent_total", "sim_sent", "sim_ok", "sim_shed", "sim_server_errors", "sim_timeouts", "sim_retries",
	"sim_suppressed", "opens", "orders", "orders_ok",
}

type Runner struct {
	C            *Clients
	OutDir       string
	Interval     time.Duration
	PushTail     time.Duration
	PushMaxWatch time.Duration
	Sleep        func(ctx context.Context, d time.Duration) error
	Now          func() time.Time
	Log          *slog.Logger
}

type Result struct {
	Scenario Scenario
	Summary  Summary
	Dir      string
}

func NewRunner(c *Clients, outDir string, log *slog.Logger) *Runner {
	return &Runner{C: c, OutDir: outDir, Log: log}
}

func (r *Runner) defaults() {
	if r.Interval <= 0 {
		r.Interval = time.Second
	}
	if r.PushTail <= 0 {
		r.PushTail = pushTail
	}
	if r.PushMaxWatch <= 0 {
		r.PushMaxWatch = pushMaxWatch
	}
	if r.Sleep == nil {
		r.Sleep = sleepCtx
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Log == nil {
		r.Log = slog.New(slog.DiscardHandler)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func terminal(s campaign.Status) bool {
	return s == campaign.StatusCompleted || s == campaign.StatusExpired || s == campaign.StatusAborted
}

func (r *Runner) Run(ctx context.Context, sc Scenario) (Result, error) {
	r.defaults()
	log := r.Log.With("scenario", sc.ID)
	if err := r.setup(ctx, sc); err != nil {
		return Result{}, fmt.Errorf("%s setup: %w", sc.ID, err)
	}
	log.Info("warming up", "warmup", sc.Warmup)
	if err := r.Sleep(ctx, sc.Warmup); err != nil {
		return Result{}, err
	}

	t0 := r.Now()
	var campaignID string
	switch {
	case sc.IsPush():
		id, err := r.C.CreateCampaign(ctx, sc)
		if err != nil {
			return Result{}, fmt.Errorf("%s create campaign: %w", sc.ID, err)
		}
		if err := r.C.StartCampaign(ctx, id); err != nil {
			return Result{}, fmt.Errorf("%s start campaign: %w", sc.ID, err)
		}
		campaignID = id
		log.Info("campaign started", "campaign_id", id)
	case sc.Chaos != nil:
		if err := r.C.SetChaos(ctx, *sc.Chaos); err != nil {
			return Result{}, fmt.Errorf("%s start chaos: %w", sc.ID, err)
		}
		log.Info("chaos started", "kind", sc.Chaos.Kind, "extra_ms", sc.Chaos.ExtraMs)
	}

	samples, last, err := r.observe(ctx, sc, t0, campaignID)
	if err != nil {
		return Result{}, err
	}
	if err := r.mergeTimeline(ctx, t0, samples); err != nil {
		return Result{}, fmt.Errorf("%s timeline: %w", sc.ID, err)
	}

	in := SummaryInput{
		ScenarioID: sc.ID, Samples: stripTimes(samples), Push: sc.IsPush(), Targeted: last.Targeted,
		PushesSent: last.Sent, PushesExpired: last.Expired,
	}
	if sc.Chaos != nil {
		in.HasChaos, in.ChaosEndS = true, sc.Chaos.Duration.Seconds()
	}
	res := Result{Scenario: sc, Summary: Summarize(in), Dir: filepath.Join(r.OutDir, sc.ID)}
	if err := writeScenario(res, in.Samples); err != nil {
		return Result{}, fmt.Errorf("%s write results: %w", sc.ID, err)
	}
	log.Info("scenario finished", "peak_offered_rps", res.Summary.PeakOfferedRPS, "max_p99_ms", res.Summary.MaxP99Ms,
		"recovery_time_s", res.Summary.RecoveryTimeS, "dir", res.Dir)
	return res, nil
}

func (r *Runner) setup(ctx context.Context, sc Scenario) error {
	if err := r.C.SetEnforce(ctx, sc.Enforce); err != nil {
		return err
	}
	if err := r.C.ResetAPI(ctx); err != nil {
		return err
	}
	if err := r.C.ConfigureSim(ctx, sc.NaiveFraction, sc.SyncMode); err != nil {
		return err
	}
	return r.C.ResetSim(ctx)
}

type timedSample struct {
	Sample
	at time.Time
}

func stripTimes(in []timedSample) []Sample {
	out := make([]Sample, len(in))
	for i, s := range in {
		out[i] = s.Sample
	}
	return out
}

func (r *Runner) observe(ctx context.Context, sc Scenario, t0 time.Time, campaignID string) ([]timedSample, campaign.View, error) {
	var samples []timedSample
	var last campaign.View
	var doneAt time.Time
	for {
		if err := r.Sleep(ctx, r.Interval); err != nil {
			return nil, last, err
		}
		now := r.Now()
		elapsed := now.Sub(t0)
		snap, err := r.C.Health(ctx)
		if err != nil {
			r.Log.Warn("health sample failed", "scenario", sc.ID, "error", err)
		} else {
			s := timedSample{at: now, Sample: Sample{
				TRel: round1(elapsed.Seconds()), OfferedRPS: snap.OfferedRPS, AdmittedRPS: snap.AdmittedRPS, ShedRPS: snap.ShedRPS,
				ErrorRate: snap.ErrorRate, P99Ms: snap.P99Ms, InFlight: snap.InFlight, Headroom: snap.Headroom, Mode: snap.Mode,
			}}
			if campaignID != "" {
				if v, err := r.C.Campaign(ctx, campaignID); err != nil {
					r.Log.Warn("campaign sample failed", "scenario", sc.ID, "error", err)
				} else {
					last = v
					s.PushRate, s.PushSentTotal = v.CurrentRate, v.Sent
					if terminal(v.Status) && doneAt.IsZero() {
						doneAt = now
					}
				}
			}
			samples = append(samples, s)
		}
		if sc.IsPush() {
			if (!doneAt.IsZero() && now.Sub(doneAt) >= r.PushTail) || elapsed >= r.PushMaxWatch {
				break
			}
		} else if elapsed >= sc.Observe {
			break
		}
	}
	return samples, last, nil
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func (r *Runner) mergeTimeline(ctx context.Context, t0 time.Time, samples []timedSample) error {
	secs, err := r.C.Timeline(ctx, t0.Unix()-10)
	if err != nil {
		return err
	}
	next := 0
	for i := range samples {
		cutoff := samples[i].at.Unix()
		for next < len(secs) && secs[next].T <= cutoff {
			if secs[next].T > t0.Unix() {
				samples[i].Sim = addSeconds(samples[i].Sim, secs[next])
			}
			next++
		}
	}
	return nil
}

func addSeconds(a, b herdsim.Second) herdsim.Second {
	a.T = b.T
	a.Sent += b.Sent
	a.SentNaive += b.SentNaive
	a.SentGuarded += b.SentGuarded
	a.OK += b.OK
	a.Shed += b.Shed
	a.ServerErrors += b.ServerErrors
	a.Timeouts += b.Timeouts
	a.NetErrors += b.NetErrors
	a.ClientErrors += b.ClientErrors
	a.Retries += b.Retries
	a.Suppressed += b.Suppressed
	a.BudgetExhausted += b.BudgetExhausted
	a.Pushes += b.Pushes
	a.Opens += b.Opens
	a.Orders += b.Orders
	a.OrdersOK += b.OrdersOK
	return a
}

func f64(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func i64(v int64) string   { return strconv.FormatInt(v, 10) }

func csvRow(p Sample) []string {
	return []string{
		f64(p.TRel), f64(p.OfferedRPS), f64(p.AdmittedRPS), f64(p.ShedRPS), f64(p.ErrorRate), f64(p.P99Ms), i64(p.InFlight),
		f64(p.Headroom), p.Mode, f64(p.PushRate), i64(p.PushSentTotal), i64(p.Sim.Sent), i64(p.Sim.OK), i64(p.Sim.Shed),
		i64(p.Sim.ServerErrors), i64(p.Sim.Timeouts), i64(p.Sim.Retries), i64(p.Sim.Suppressed), i64(p.Sim.Opens),
		i64(p.Sim.Orders), i64(p.Sim.OrdersOK),
	}
}

func WriteSamplesCSV(path string, samples []Sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(CSVHeader); err != nil {
		_ = f.Close()
		return err
	}
	for _, p := range samples {
		if err := w.Write(csvRow(p)); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeScenario(res Result, samples []Sample) error {
	if err := os.MkdirAll(res.Dir, 0o755); err != nil {
		return err
	}
	if err := WriteSamplesCSV(filepath.Join(res.Dir, "samples.csv"), samples); err != nil {
		return err
	}
	data, err := json.MarshalIndent(res.Summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(res.Dir, "summary.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return writeCharts(res.Dir, res.Scenario, samples)
}

func writeCharts(dir string, sc Scenario, samples []Sample) error {
	col := func(f func(Sample) float64) (xs, ys []float64) {
		for _, p := range samples {
			xs, ys = append(xs, p.TRel), append(ys, f(p))
		}
		return xs, ys
	}
	series := func(name string, f func(Sample) float64) Series {
		x, y := col(f)
		return Series{Name: name, X: x, Y: y}
	}
	charts := []struct {
		file, title, y string
		series         []Series
	}{
		{"rps.png", sc.ID + " " + sc.Name + ": requests per second", "requests/s", []Series{
			series("offered", func(p Sample) float64 { return p.OfferedRPS }),
			series("admitted", func(p Sample) float64 { return p.AdmittedRPS }),
			series("shed", func(p Sample) float64 { return p.ShedRPS }),
		}},
		{"latency.png", sc.ID + " " + sc.Name + ": p99 latency", "ms", []Series{
			series("p99", func(p Sample) float64 { return p.P99Ms }),
		}},
		{"failures.png", sc.ID + " " + sc.Name + ": client failures and retries", "per second", []Series{
			series("failures", func(p Sample) float64 { return float64(p.Sim.ServerErrors + p.Sim.Timeouts + p.Sim.NetErrors) }),
			series("retries", func(p Sample) float64 { return float64(p.Sim.Retries) }),
		}},
	}
	if sc.IsPush() {
		charts = append(charts, struct {
			file, title, y string
			series         []Series
		}{"push.png", sc.ID + " " + sc.Name + ": push progress", "pushes/s, sent / 100", []Series{
			series("push rate", func(p Sample) float64 { return p.PushRate }),
			series("sent / 100", func(p Sample) float64 { return float64(p.PushSentTotal) / 100 }),
		}})
	}
	for _, c := range charts {
		if err := LineChart(filepath.Join(dir, c.file), c.title, "seconds since start", c.y, c.series); err != nil {
			return err
		}
	}
	return nil
}
