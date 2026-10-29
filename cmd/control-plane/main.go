package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AbhishekBasnet/stampede-guard/internal/controlplane/app"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/logging"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func main() {
	cfg, err := app.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "control-plane: invalid configuration:", err)
		os.Exit(1)
	}
	log := logging.New(cfg.LogLevel, "control-plane")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, clock.Real{}, log)
	if err != nil {
		log.Error("control-plane failed to start", "error", err)
		os.Exit(1)
	}
	if err := a.Run(ctx); err != nil {
		log.Error("control-plane stopped", "error", err)
		os.Exit(1)
	}
}
