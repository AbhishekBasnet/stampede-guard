package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AbhishekBasnet/stampede-guard/internal/herdsim"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/logging"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func main() {
	cfg, err := herdsim.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "herd-sim: invalid configuration:", err)
		os.Exit(1)
	}
	log := logging.New(cfg.LogLevel, "herd-sim")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := herdsim.New(cfg, clock.Real{}, log).Run(ctx); err != nil {
		log.Error("herd-sim stopped", "error", err)
		os.Exit(1)
	}
}
