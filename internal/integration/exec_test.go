//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerExec(t *testing.T) {
	env := newEnv(t)
	id, _ := startAlpineSleep(t, env)

	out, err := env.docker(t, 30*time.Second, "exec", id, "echo", "dik-exec-out")
	require.NoError(t, err, "docker exec output:\n%s", out)
	assert.Contains(t, out, "dik-exec-out")
}

func TestDockerExecStdin(t *testing.T) {
	env := newEnv(t)
	id, _ := startAlpineSleep(t, env)

	out, err := env.dockerStdin(t, 30*time.Second,
		strings.NewReader("dik-exec-stdin\n"),
		"exec", "-i", id, "cat",
	)
	require.NoError(t, err, "docker exec -i output:\n%s", out)
	assert.Contains(t, out, "dik-exec-stdin")
}

func TestDockerExecNonZeroExit(t *testing.T) {
	env := newEnv(t)
	id, _ := startAlpineSleep(t, env)

	out, err := env.docker(t, 30*time.Second, "exec", id, "sh", "-c", "exit 9")
	require.Error(t, err, "expected non-zero exit; output:\n%s", out)
}
