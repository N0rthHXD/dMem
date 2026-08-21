package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/daemon"
)

func main() {
	cfg, err := config.Parse(flag.CommandLine, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d, err := daemon.Start(ctx, cfg)
	if err != nil {
		slog.Error("daemon startup failed", "error", err)
		os.Exit(1)
	}
	slog.Info("daemon ready")

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	defer cancel()
	if err := d.Close(shutdownCtx); err != nil {
		slog.Error("daemon shutdown failed", "error", err)
		os.Exit(1)
	}
}
