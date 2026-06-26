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

// TestMain wipes pods leaked by a previous failed run so TestDockerPsEmpty etc. start clean.
func TestMain(m *testing.M) {
	wipeStalePods()
	os.Exit(m.Run())
}

func wipeStalePods() {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		return
	}
	conn, err := k8s.Connect(k8s.ClientConfig{KubeconfigPath: kubeconfig})
	if err != nil {
		return
	}
	pods := k8s.NewPods(conn.Clientset, testNamespace)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := pods.List(ctx)
	if err != nil {
		return
	}
	for i := range list {
		_ = pods.Delete(ctx, list[i].Name, 0)
	}
}

type testEnv struct {
	Pods       *k8s.Pods
	SocketPath string
}

// newEnv starts the daemon on a UNIX socket so tests can drive `docker -H ...`.
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

	// /tmp-rooted: darwin's sun_path is 104 bytes; t.TempDir() blows past it.
	socketDir, err := os.MkdirTemp("/tmp", "dik")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "s")
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

	return &testEnv{Pods: pods, SocketPath: socketPath}
}

// docker runs the docker CLI against the daemon with a per-call timeout.
func (e *testEnv) docker(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", append([]string{"-H", "unix://" + e.SocketPath}, args...)...)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), context.DeadlineExceeded
	}
	return string(out), err
}

func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", path)
}

func cleanupPod(t *testing.T, pods *k8s.Pods, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = pods.Delete(ctx, name, 0)
	})
}

func randSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	now := time.Now().UnixNano()
	var b [6]byte
	for i := range b {
		b[i] = alphabet[uint(now>>(uint(i)*5))%uint(len(alphabet))]
	}
	return string(b[:])
}
