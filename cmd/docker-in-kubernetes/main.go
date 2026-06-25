// Command docker-in-kubernetes runs the daemon that exposes a
// Docker-compatible HTTP API on a UNIX socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bpaquet/docker-in-kubernetes/internal/logutil"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
	"github.com/bpaquet/docker-in-kubernetes/internal/sockutil"
)

// version is the daemon version reported in /version. Override at build time
// with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	socketPath := flag.String("socket", "/tmp/docker-in-kubernetes.sock", "UNIX socket path to listen on")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	logger, err := logutil.New(os.Stderr, *logLevel)
	if err != nil {
		return err
	}

	listener, err := sockutil.ListenUnix(*socketPath)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           server.New(server.Config{DaemonVersion: version, Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "socket", *socketPath, "version", version)
		fmt.Fprintf(os.Stderr, "\n  export DOCKER_HOST=unix://%s\n\n", *socketPath)
		serveErr <- httpServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", "err", err)
	}
	return nil
}
