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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
	"github.com/bpaquet/docker-in-kubernetes/internal/sockutil"
)

const testNamespace = "docker-in-kubernetes"

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
// Returns combined stdout+stderr and the exec error (or context.DeadlineExceeded
// if the CLI hung).
func (e *testEnv) docker(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", append([]string{"-H", "unix://" + e.SocketPath}, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+e.SocketPath)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), context.DeadlineExceeded
	}
	return string(out), err
}

// dialSocket connects to the UNIX socket with a timeout.
func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", path)
}
