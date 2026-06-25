//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// TestDockerVersion confirms the daemon answers /_ping and /version via the
// real CLI before the heavier redis test runs.
func TestDockerVersion(t *testing.T) {
	env := newEnv(t)
	out := env.mustDocker(t, 30*time.Second, "version")
	assert.Contains(t, out, "1.43", "expected our advertised API version to appear in `docker version`")
}

// TestDockerRunDetachedRedis drives the headline use case end-to-end through
// the real docker CLI. This is the test that catches the "docker run -d hangs"
// class of bug.
func TestDockerRunDetachedRedis(t *testing.T) {
	env := newEnv(t)

	name := "it-redis-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	hostPort := freeLocalPort(t)

	// docker run -d -p HOST:6379 --name <name> redis:7-alpine
	runOut, err := env.docker(t, 2*time.Minute,
		"run", "-d",
		"-p", strconv.Itoa(hostPort)+":6379",
		"--name", name,
		"redis:7-alpine",
	)
	require.NoError(t, err, "docker run output:\n%s", runOut)
	require.NotEmpty(t, strings.TrimSpace(runOut), "expected a container ID on stdout")

	containerID := strings.TrimSpace(strings.Split(runOut, "\n")[0])
	t.Logf("container ID: %s", containerID)

	// Ping redis through the forwarder.
	require.NoError(t, redisPing(t, hostPort), "redis should be reachable via the local forwarder")

	// docker ps must list this container.
	psOut := env.mustDocker(t, 30*time.Second, "ps")
	assert.Contains(t, psOut, name, "docker ps should include %s; got:\n%s", name, psOut)

	// docker logs must return something.
	logsOut, err := env.docker(t, 30*time.Second, "logs", containerID)
	require.NoError(t, err)
	assert.NotEmpty(t, logsOut, "expected some log output from redis")

	// docker rm -f deletes the pod and frees the port.
	rmOut, err := env.docker(t, 30*time.Second, "rm", "-f", containerID)
	require.NoError(t, err, "docker rm output:\n%s", rmOut)

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 30*time.Second, 200*time.Millisecond, "pod should be gone after docker rm -f")

	_, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(hostPort), 200*time.Millisecond)
	require.Error(t, err, "host port should be free after docker rm -f")
}

// TestDockerRunInteractiveRejected verifies non-detached `docker run` fails
// fast with our "use -d" message rather than the CLI's cryptic
// "unable to upgrade to tcp" line.
func TestDockerRunInteractiveRejected(t *testing.T) {
	env := newEnv(t)
	name := "it-noattach-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 30*time.Second, "run", "--name", name, "redis:7-alpine")
	require.Error(t, err, "docker run (no -d) should fail")
	assert.Contains(t, strings.ToLower(out), "use -d")
}

// TestDockerRunBogusImageFailsFast asserts ImagePullBackOff is surfaced quickly.
func TestDockerRunBogusImageFailsFast(t *testing.T) {
	env := newEnv(t)
	name := "it-bogus-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	start := time.Now()
	out, err := env.docker(t, 90*time.Second,
		"run", "-d", "--name", name,
		"docker-in-kubernetes-bogus-image-that-does-not-exist:nope",
	)
	elapsed := time.Since(start)
	require.Error(t, err, "docker run on bogus image should fail; output:\n%s", out)
	assert.Less(t, elapsed, 90*time.Second, "fail-fast should not approach the timeout")
	assert.Contains(t, strings.ToLower(out), "imagepull")
}

// --- helpers ----------------------------------------------------------------

func redisPing(t *testing.T, hostPort int) error {
	t.Helper()
	// The forwarder may take a moment to start accepting connections after
	// the pod becomes Ready; retry for a few seconds.
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(hostPort), 2*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("PING\r\n")); err != nil {
			_ = conn.Close()
			lastErr = err
			continue
		}
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if strings.Contains(string(buf[:n]), "PONG") {
			return nil
		}
		lastErr = fmt.Errorf("unexpected redis reply: %q", string(buf[:n]))
	}
	return lastErr
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
