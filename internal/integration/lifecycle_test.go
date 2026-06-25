//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// startAlpineSleep brings up a small alpine pod via docker run -d so the
// lifecycle tests below don't pay the cost of pulling redis.
func startAlpineSleep(t *testing.T, env *testEnv) (containerID, podName string) {
	t.Helper()
	podName = "it-alp-" + randSuffix()
	cleanupPod(t, env.Pods, podName)
	out, err := env.docker(t, 60*time.Second, "run", "-d", "--name", podName, "alpine:3", "sleep", "300")
	require.NoError(t, err, "docker run output:\n%s", out)
	containerID = strings.TrimSpace(strings.Split(out, "\n")[0])
	require.NotEmpty(t, containerID)
	return containerID, podName
}

func TestDockerKillTerminatesContainer(t *testing.T) {
	env := newEnv(t)
	id, name := startAlpineSleep(t, env)

	out, err := env.docker(t, 30*time.Second, "kill", id)
	require.NoError(t, err, "docker kill output:\n%s", out)

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 30*time.Second, 200*time.Millisecond, "pod should be gone after docker kill")
}

func TestDockerRmForceTerminatesAndRemoves(t *testing.T) {
	env := newEnv(t)
	id, name := startAlpineSleep(t, env)

	out, err := env.docker(t, 30*time.Second, "rm", "-f", id)
	require.NoError(t, err, "docker rm -f output:\n%s", out)

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 30*time.Second, 200*time.Millisecond, "pod should be gone after docker rm -f")
}

// TestDockerRmAfterKillIsNoOp verifies the design's "rm is no-op if pod is
// already gone" rule: `docker kill` then `docker rm` should succeed, even
// though `kill` deleted the underlying pod and our /containers/{id}/json
// inspect would 404.
func TestDockerRmAfterKillIsNoOp(t *testing.T) {
	env := newEnv(t)
	id, name := startAlpineSleep(t, env)

	out, err := env.docker(t, 30*time.Second, "kill", id)
	require.NoError(t, err, "docker kill output:\n%s", out)

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 30*time.Second, 200*time.Millisecond)

	rmOut, err := env.docker(t, 15*time.Second, "rm", id)
	require.NoError(t, err, "docker rm after kill should be a no-op; output:\n%s", rmOut)
}

func TestDockerPsListsRunningContainer(t *testing.T) {
	env := newEnv(t)
	_, name := startAlpineSleep(t, env)

	out, err := env.docker(t, 15*time.Second, "ps")
	require.NoError(t, err, "docker ps output:\n%s", out)
	assert.Contains(t, out, name, "docker ps should list %s", name)
}

func TestDockerLogsReturnsContainerOutput(t *testing.T) {
	env := newEnv(t)
	name := "it-log-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	runOut, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name, "alpine:3",
		"sh", "-c", "echo hello-from-alpine; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", runOut)
	id := strings.TrimSpace(strings.Split(runOut, "\n")[0])

	require.Eventually(t, func() bool {
		out, err := env.docker(t, 15*time.Second, "logs", id)
		return err == nil && strings.Contains(out, "hello-from-alpine")
	}, 30*time.Second, 500*time.Millisecond, "docker logs should eventually return the echo line")
}

func TestDockerInspectReturnsContainerFields(t *testing.T) {
	env := newEnv(t)
	id, name := startAlpineSleep(t, env)

	out, err := env.docker(t, 15*time.Second, "inspect", id)
	require.NoError(t, err, "docker inspect output:\n%s", out)
	assert.Contains(t, out, name, "docker inspect should mention the container name")
	assert.Contains(t, out, id, "docker inspect should mention the container id")
	assert.Contains(t, out, `"Image": "alpine:3"`, "docker inspect should include the image")
}

// TestDockerPortMapping verifies that -p HOST:CONTAINER bindings are echoed
// back in docker ps and inspect, even when the workload itself doesn't need
// port-forwarding to be verified.
func TestDockerPortMapping(t *testing.T) {
	env := newEnv(t)
	name := "it-prt-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	hostPort := freeLocalPort(t)
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"-p", strconv.Itoa(hostPort)+":80",
		"alpine:3", "sleep", "60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)
	id := strings.TrimSpace(strings.Split(out, "\n")[0])

	psOut, err := env.docker(t, 15*time.Second, "ps")
	require.NoError(t, err, "docker ps output:\n%s", psOut)
	assert.Contains(t, psOut, "80/tcp")

	inspectOut, err := env.docker(t, 15*time.Second, "inspect", id)
	require.NoError(t, err)
	assert.Contains(t, inspectOut, `"HostPort": "`+strconv.Itoa(hostPort)+`"`)
}
