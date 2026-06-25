//go:build integration

package integration_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
	"github.com/bpaquet/docker-in-kubernetes/internal/sockutil"
)

const testNamespace = "docker-in-kubernetes"

// testEnv bundles everything an integration test needs to drive `docker -H ...`.
type testEnv struct {
	Pods       *k8s.Pods
	Registry   *forwarder.Registry
	SocketPath string
}

// newEnv starts the daemon on a UNIX socket and returns helpers. Tests run
// the real `docker` CLI against this socket.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	if os.Getenv("KUBECONFIG") == "" {
		t.Fatal("KUBECONFIG must be set; run via `mise run integration-test`")
	}

	conn, err := k8s.Connect(k8s.ClientConfig{KubeconfigPath: os.Getenv("KUBECONFIG")})
	require.NoError(t, err)

	pods := k8s.NewPods(conn.Clientset, testNamespace)
	pods.SetPollInterval(200 * time.Millisecond)
	pods.SetReadyTimeout(2 * time.Minute)
	registry := forwarder.NewRegistry()
	fw := forwarder.NewSPDYForwarder(conn.Clientset, conn.REST, slog.Default())

	socketPath := filepath.Join(t.TempDir(), "dik.sock")
	listener, err := sockutil.ListenUnix(socketPath)
	require.NoError(t, err)

	httpServer := &http.Server{
		Handler: server.New(server.Config{
			DaemonVersion: "integration-test",
			Pods:          pods,
			Forwarder:     fw,
			Forwards:      registry,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = registry.Shutdown()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Logf("daemon serve returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Log("daemon did not exit cleanly within 2s")
		}
	})

	return &testEnv{Pods: pods, Registry: registry, SocketPath: socketPath}
}

// docker runs the docker CLI against the test daemon with a per-call timeout.
// Returns combined stdout+stderr and the exit error.
func (e *testEnv) docker(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", append([]string{"-H", "unix://" + e.SocketPath}, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+e.SocketPath)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), context.DeadlineExceeded
	}
	return string(out), err
}

// mustDocker fails the test if the docker CLI errors or times out.
func (e *testEnv) mustDocker(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	out, err := e.docker(t, timeout, args...)
	if err != nil {
		t.Fatalf("docker %s failed: %v\nOutput:\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// cleanupPod best-effort deletes a pod by name; used as t.Cleanup.
func cleanupPod(t *testing.T, pods *k8s.Pods, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = pods.Delete(ctx, name, 0)
	})
}

// freeLocalPort returns a 127.0.0.1 TCP port that is free at the moment of
// the call. There is an unavoidable race with another process binding the
// same port before the daemon does.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
