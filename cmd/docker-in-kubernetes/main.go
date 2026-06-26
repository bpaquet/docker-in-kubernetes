// Command docker-in-kubernetes runs the daemon.
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

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/logutil"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
	"github.com/bpaquet/docker-in-kubernetes/internal/sockutil"
)

// version is overridden at build time with -ldflags "-X main.version=...".
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
	namespace := flag.String("namespace", "docker-in-kubernetes", "Kubernetes namespace to manage pods in")
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (defaults to KUBECONFIG env, then ~/.kube/config)")
	kubeContext := flag.String("context", "", "kubeconfig context to use (defaults to current-context)")
	flag.Parse()

	logger, err := logutil.New(os.Stderr, *logLevel)
	if err != nil {
		return err
	}

	conn, err := k8s.Connect(k8s.ClientConfig{
		KubeconfigPath: *kubeconfig,
		Context:        *kubeContext,
	})
	if err != nil {
		return fmt.Errorf("kubernetes connect: %w", err)
	}
	logger.Info("kubernetes connected", "mode", conn.Mode.String(), "namespace", *namespace)

	pods := k8s.NewPods(conn.Clientset, *namespace).WithREST(conn.REST)

	var fw server.PortForwarder
	switch conn.Mode {
	case k8s.ModeInCluster:
		fw = forwarder.NewTCPForwarder(pods, logger)
	default:
		fw = forwarder.NewSPDYForwarder(conn.Clientset, conn.REST, logger)
	}

	registry := forwarder.NewRegistry()

	listener, err := sockutil.ListenUnix(*socketPath)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler: server.New(server.Config{
			DaemonVersion: version,
			Logger:        logger,
			Pods:          pods,
			Forwarder:     fw,
			Forwards:      registry,
		}),
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
	if err := registry.Shutdown(); err != nil {
		logger.Warn("forwarder shutdown failed", "err", err)
	}
	return nil
}
