package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/internal/experiment"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/logging"
)

func main() {
	defToken := os.Getenv("ADMIN_TOKEN")
	if defToken == "" {
		defToken = "dev-admin-token"
	}
	scenario := flag.String("scenario", "all", "all or S1 to S7")
	out := flag.String("out", "results", "output directory")
	api := flag.String("api", "http://localhost:8080", "demo-api base URL")
	cp := flag.String("cp", "http://localhost:8090", "control-plane base URL")
	sim := flag.String("sim", "http://localhost:8070", "herd-sim base URL")
	token := flag.String("token", defToken, "admin bearer token")
	list := flag.Bool("list", false, "list scenarios and exit")
	flag.Parse()

	if *list {
		for _, s := range experiment.All() {
			fmt.Printf("%s  %-18s %s\n", s.ID, s.Name, s.Description)
		}
		return
	}
	scenarios, err := experiment.Select(*scenario)
	if err != nil {
		fmt.Fprintln(os.Stderr, "experiment:", err)
		os.Exit(2)
	}

	log := logging.New("info", "experiment")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	dir := filepath.Join(*out, started.Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error("create output directory", "error", err)
		os.Exit(1)
	}
	clients := experiment.NewClients(*api, *cp, *sim, *token)
	runner := experiment.NewRunner(clients, dir, log)

	var results []experiment.Result
	exit := 0
	for _, sc := range scenarios {
		res, err := runner.Run(ctx, sc)
		if err != nil {
			log.Error("scenario failed", "scenario", sc.ID, "error", err)
			exit = 1
			break
		}
		results = append(results, res)
		if err := writeReport(ctx, clients, dir, started, *api, *cp, *sim, results); err != nil {
			log.Error("write report", "error", err)
			exit = 1
			break
		}
	}
	restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clients.SetSyncMode(restoreCtx, "spread"); err != nil {
		log.Warn("could not restore sync mode", "error", err)
	}
	if len(results) > 0 {
		log.Info("report written", "path", filepath.Join(dir, "REPORT.md"))
	}
	os.Exit(exit)
}

func writeReport(ctx context.Context, c *experiment.Clients, dir string, started time.Time, api, cp, sim string, results []experiment.Result) error {
	stats, err := c.SimStats(ctx)
	if err != nil {
		stats = nil
	}
	return experiment.WriteReport(dir, experiment.ReportMeta{Date: started, APIURL: api, CPURL: cp, SimURL: sim, SimStats: stats}, results)
}
