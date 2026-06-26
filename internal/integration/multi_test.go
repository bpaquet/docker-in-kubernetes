//go:build integration

package integration_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Second run with the same HOST port must fail.
func TestPortConflict(t *testing.T) {
	env := newEnv(t)

	hostPort := freeLocalPort(t)

	a := "it-pc-a-" + randSuffix()
	cleanupPod(t, env.Pods, a)
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", a,
		"-p", strconv.Itoa(hostPort)+":6379",
		"redis:7-alpine",
	)
	require.NoError(t, err, "first run output:\n%s", out)

	b := "it-pc-b-" + randSuffix()
	cleanupPod(t, env.Pods, b)
	out, err = env.docker(t, 30*time.Second,
		"run", "-d", "--name", b,
		"-p", strconv.Itoa(hostPort)+":6379",
		"redis:7-alpine",
	)
	require.Error(t, err, "second run on same host port should fail; output:\n%s", out)
	assert.Contains(t, strings.ToLower(out), "unable to listen",
		"expected port-in-use error; got:\n%s", out)
}

// Two -p mappings on the same container, both reachable.
func TestMultiplePortMappings(t *testing.T) {
	env := newEnv(t)
	name := "it-mp-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	hostA := freeLocalPort(t)
	hostB := freeLocalPort(t)

	out, err := env.docker(t, 90*time.Second,
		"run", "-d", "--name", name,
		"-p", strconv.Itoa(hostA)+":6379",
		"-p", strconv.Itoa(hostB)+":6379",
		"redis:7-alpine",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	require.NoError(t, redisPing(t, hostA))
	require.NoError(t, redisPing(t, hostB))
}

// Two containers running concurrently: ps lists both; kill one, the other stays.
func TestConcurrentContainers(t *testing.T) {
	env := newEnv(t)
	a := "it-cc-a-" + randSuffix()
	b := "it-cc-b-" + randSuffix()
	cleanupPod(t, env.Pods, a)
	cleanupPod(t, env.Pods, b)

	for _, n := range []string{a, b} {
		out, err := env.docker(t, 60*time.Second, "run", "-d", "--name", n, "alpine:3", "sleep", "60")
		require.NoError(t, err, "run %s output:\n%s", n, out)
	}

	psOut, err := env.docker(t, 15*time.Second, "ps")
	require.NoError(t, err)
	assert.Contains(t, psOut, a)
	assert.Contains(t, psOut, b)

	out, err := env.docker(t, 15*time.Second, "kill", a)
	require.NoError(t, err, "kill output:\n%s", out)

	require.Eventually(t, func() bool {
		out, err := env.docker(t, 10*time.Second, "ps")
		return err == nil && !strings.Contains(out, a) && strings.Contains(out, b)
	}, 15*time.Second, 200*time.Millisecond, "after kill, %s should be gone and %s still listed", a, b)
}
