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

// TestDockerRunDetachedRedis is the headline use case: `docker run -d -p
// HOST:6379 redis:7-alpine` brings up a pod, opens the local forwarder,
// returns a container ID. redis-cli PING reaches the pod through the
// forwarder. docker rm -f cleans everything up.
//
// Tight per-call timeouts so an interactive hang shows up immediately rather
// than blocking the whole 60s package timeout.
func TestDockerRunDetachedRedis(t *testing.T) {
	env := newEnv(t)
	name := "it-redis-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	hostPort := freeLocalPort(t)

	runOut, err := env.docker(t, 45*time.Second,
		"run", "-d",
		"-p", strconv.Itoa(hostPort)+":6379",
		"--name", name,
		"redis:7-alpine",
	)
	require.NoError(t, err, "docker run -d output:\n%s", runOut)
	require.NotEmpty(t, strings.TrimSpace(runOut), "expected a container ID")

	containerID := strings.TrimSpace(strings.Split(runOut, "\n")[0])
	t.Logf("container ID: %s", containerID)

	require.NoError(t, redisPing(t, hostPort), "redis should answer PING through the forwarder")

	rmOut, err := env.docker(t, 15*time.Second, "rm", "-f", containerID)
	require.NoError(t, err, "docker rm output:\n%s", rmOut)

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 15*time.Second, 200*time.Millisecond, "pod should be gone")

	_, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(hostPort), 200*time.Millisecond)
	assert.Error(t, err, "host port should be free after docker rm -f")
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func redisPing(t *testing.T, hostPort int) error {
	t.Helper()
	// Forwarder may need a moment to start accepting connections after the
	// pod becomes Ready; retry briefly.
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(hostPort), 2*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
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
