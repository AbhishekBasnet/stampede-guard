package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AbhishekBasnet/stampede-guard/internal/demoapi"
	"github.com/AbhishekBasnet/stampede-guard/internal/platform/logging"
	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func main() {
	cfg, err := demoapi.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo-api: invalid configuration:", err)
		os.Exit(1)
	}
	log := logging.New(cfg.LogLevel, "demo-api")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := demoapi.New(cfg, clock.Real{}, log).Run(ctx); err != nil {
		log.Error("demo-api stopped", "error", err)
		os.Exit(1)
	}
}
