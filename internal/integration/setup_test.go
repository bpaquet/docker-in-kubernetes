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

// TestMain wipes any managed pods left over from a previous run before any
// test executes, so TestDockerPsEmpty and friends start from a clean slate.
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

	// Use /tmp-rooted dir so the socket path fits in sun_path (104 bytes on
	// darwin). t.TempDir() on darwin lives under /var/folders/... which is
	// much longer than that.
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

// docker runs the docker CLI against the test daemon with a per-call timeout.
// Returns combined stdout+stderr and the exec error (or context.DeadlineExceeded
// if the CLI hung).
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

// dialSocket connects to the UNIX socket with a timeout.
func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", path)
}

// cleanupPod best-effort deletes a pod by name on test completion.
func cleanupPod(t *testing.T, pods *k8s.Pods, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = pods.Delete(ctx, name, 0)
	})
}

// randSuffix returns a 6-char lowercase-alphanumeric suffix derived from the
// monotonic clock; collisions across parallel tests are vanishingly rare.
func randSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	now := time.Now().UnixNano()
	var b [6]byte
	for i := range b {
		b[i] = alphabet[uint(now>>(uint(i)*5))%uint(len(alphabet))]
	}
	return string(b[:])
}
