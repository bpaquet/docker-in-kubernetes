//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// docker run (no -d): attaches stdout, prints output, exits.
func TestDockerRunNonDetachedStreamsOutput(t *testing.T) {
	env := newEnv(t)
	name := "it-att-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second, "run", "--name", name, "alpine:3", "echo", "dik-attach-out")
	require.NoError(t, err, "docker run output:\n%s", out)
	assert.Contains(t, out, "dik-attach-out")
}

// docker run --rm: pod is deleted automatically after the run exits.
func TestDockerRunRm(t *testing.T) {
	env := newEnv(t)
	name := "it-rm-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second, "run", "--rm", "--name", name, "alpine:3", "echo", "hi")
	require.NoError(t, err, "docker run --rm output:\n%s", out)
	assert.Contains(t, out, "hi")

	require.Eventually(t, func() bool {
		_, err := env.Pods.Get(context.Background(), name)
		return errors.Is(err, k8s.ErrNotFound)
	}, 30*time.Second, 200*time.Millisecond, "pod should be gone after --rm")
}

// docker run with a command that exits non-zero: docker CLI surfaces the exit.
func TestDockerRunNonZeroExit(t *testing.T) {
	env := newEnv(t)
	name := "it-exit-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second, "run", "--name", name, "alpine:3", "sh", "-c", "exit 7")
	require.Error(t, err, "docker run with exit 7 should fail; output:\n%s", out)
}

// `echo 'echo 3' | docker run --rm -i bash` — pipe a command into bash, see
// the result on stdout, container is auto-removed. Real docker prints "3".
func TestDockerRunRmInteractiveBash(t *testing.T) {
	env := newEnv(t)
	name := "it-rmib-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.dockerStdin(t, 90*time.Second,
		strings.NewReader("echo 3\n"),
		"run", "--rm", "-i", "--name", name, "bash",
	)
	require.NoError(t, err, "docker run --rm -i bash output:\n%s", out)
	assert.Contains(t, out, "3", "expected bash to echo 3; got:\n%s", out)
}

// docker run -i with cat: stdin is piped in, stdout streams back.
func TestDockerRunInteractiveStdin(t *testing.T) {
	env := newEnv(t)
	name := "it-stdin-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.dockerStdin(t, 60*time.Second,
		strings.NewReader("dik-stdin-roundtrip\n"),
		"run", "-i", "--rm", "--name", name, "alpine:3", "cat",
	)
	require.NoError(t, err, "docker run -i output:\n%s", out)
	assert.Contains(t, out, "dik-stdin-roundtrip")
}
