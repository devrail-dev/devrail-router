package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devrail-dev/devrail-router/internal/config"
	"github.com/devrail-dev/devrail-router/internal/server"
)

const version = "0.1.0-dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}

	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "check":
		return check(args[1:])
	case "version":
		fmt.Println(version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultPath, "path to router config")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		return 1
	}

	handler, err := server.New(cfg)
	if err != nil {
		slog.Error("create server", "error", err)
		return 1
	}

	httpServer := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting devrail router", "address", cfg.Server.Address)
		errCh <- httpServer.ListenAndServe()
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			return 1
		}
	case sig := <-signalCh:
		slog.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			slog.Error("shutdown failed", "error", err)
			return 1
		}
	}

	return 0
}

func check(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultPath, "path to router config")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config invalid", "error", err)
		return 1
	}

	slog.Info("config ok", "address", cfg.Server.Address, "models", len(cfg.Models), "backends", len(cfg.Backends))
	return 0
}

func usage() {
	fmt.Fprintf(os.Stderr, `DevRail Router %s

Usage:
  devrail-router serve [-config path]
  devrail-router check [-config path]
  devrail-router version

`, version)
}
