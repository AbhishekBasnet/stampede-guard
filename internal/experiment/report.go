package experiment

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ReportMeta struct {
	Date                  time.Time
	APIURL, CPURL, SimURL string
	SimStats              json.RawMessage
}

func num(v float64) string { return fmt.Sprintf("%.1f", v) }

func special(v float64, unit string) string {
	switch v {
	case NotApplicable:
		return "n/a"
	case NotRecovered:
		return "not reached"
	}
	return num(v) + unit
}

func percent(v float64) string {
	if v == NotApplicable {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

func WriteReport(dir string, meta ReportMeta, results []Result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Stampede Guard experiment report\n\nRun on %s.\n\n", meta.Date.Format("2006-01-02 15:04:05 MST"))

	b.WriteString("## Configuration\n\n")
	fmt.Fprintf(&b, "- demo-api: `%s`\n- control-plane: `%s`\n- herd-sim: `%s`\n", meta.APIURL, meta.CPURL, meta.SimURL)
	if len(meta.SimStats) > 0 {
		var stats struct {
			Devices int             `json:"devices"`
			Config  json.RawMessage `json:"config"`
		}
		if json.Unmarshal(meta.SimStats, &stats) == nil {
			fmt.Fprintf(&b, "- virtual devices: %d\n- herd-sim config at the end of the run: `%s`\n", stats.Devices, stats.Config)
		}
	}

	b.WriteString("\n## Comparison\n\n")
	b.WriteString("| Scenario | Peak offered rps | Peak admitted rps | Max p99 ms | Seconds p99 > 1 s | Client failures | Shed | Retries | Suppressed | Order success | Time to 95% sent | Pushes sent / expired | Recovery |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range results {
		s := r.Summary
		fmt.Fprintf(&b, "| %s %s | %s | %s | %s | %d | %d | %d | %d | %d | %s | %s | %d / %d | %s |\n",
			r.Scenario.ID, r.Scenario.Name, num(s.PeakOfferedRPS), num(s.PeakAdmittedRPS), num(s.MaxP99Ms), s.SecondsP99Over1s,
			s.ClientFailures, s.ShedResponses, s.Retries, s.Suppressed, percent(s.OrderSuccessRate),
			special(s.TimeTo95PctSentS, " s"), s.PushesSent, s.PushesExpired, special(s.RecoveryTimeS, " s"))
	}

	for _, r := range results {
		s := r.Summary
		fmt.Fprintf(&b, "\n## %s %s\n\n%s\n\n**Hypothesis:** %s\n\n", r.Scenario.ID, r.Scenario.Name, r.Scenario.Description, r.Scenario.Hypothesis)
		fmt.Fprintf(&b, "- guard enforcement: %t, naive clients: %.0f%%, sync mode: %s\n", r.Scenario.Enforce, r.Scenario.NaiveFraction*100, r.Scenario.SyncMode)
		fmt.Fprintf(&b, "- peak offered %s rps, peak admitted %s rps, max p99 %s ms (%d seconds above 1 s)\n", num(s.PeakOfferedRPS), num(s.PeakAdmittedRPS), num(s.MaxP99Ms), s.SecondsP99Over1s)
		fmt.Fprintf(&b, "- client failures %d, shed responses %d, retries %d, suppressed %d\n", s.ClientFailures, s.ShedResponses, s.Retries, s.Suppressed)
		fmt.Fprintf(&b, "- orders %d of %d succeeded (%s)\n", s.OrdersOK, s.OrdersAttempted, percent(s.OrderSuccessRate))
		if r.Scenario.IsPush() {
			fmt.Fprintf(&b, "- pushes sent %d, expired %d, time to 95%% sent %s\n", s.PushesSent, s.PushesExpired, special(s.TimeTo95PctSentS, " s"))
		}
		if r.Scenario.Chaos != nil {
			fmt.Fprintf(&b, "- recovery after chaos: %s\n", special(s.RecoveryTimeS, " s"))
		}
		fmt.Fprintf(&b, "- seconds in mode: %v\n\n", s.SecondsInMode)
		images := []string{"rps", "latency", "failures"}
		if r.Scenario.IsPush() {
			images = append(images, "push")
		}
		for _, img := range images {
			fmt.Fprintf(&b, "![%s %s](%s/%s.png)\n\n", r.Scenario.ID, img, r.Scenario.ID, img)
		}
	}
	return os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte(b.String()), 0o644)
}
