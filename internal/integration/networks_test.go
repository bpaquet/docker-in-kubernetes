//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerNetworkCreateInspectRm(t *testing.T) {
	env := newEnv(t)
	name := "it-net-" + randSuffix()

	out, err := env.docker(t, 10*time.Second, "network", "create", name)
	require.NoError(t, err, "docker network create failed:\n%s", out)
	assert.NotEmpty(t, strings.TrimSpace(out))

	out, err = env.docker(t, 10*time.Second, "network", "inspect", name, "--format", "{{.Name}} {{.Driver}} {{.Scope}}")
	require.NoError(t, err, "docker network inspect failed:\n%s", out)
	assert.Contains(t, out, name)
	assert.Contains(t, out, "bridge")
	assert.Contains(t, out, "local")

	out, err = env.docker(t, 10*time.Second, "network", "ls")
	require.NoError(t, err, "docker network ls failed:\n%s", out)
	assert.Contains(t, out, name)

	out, err = env.docker(t, 10*time.Second, "network", "rm", name)
	require.NoError(t, err, "docker network rm failed:\n%s", out)
	assert.Contains(t, out, name)
}

func TestDockerNetworkInspectMissing(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 10*time.Second, "network", "inspect", "nope-"+randSuffix())
	require.Error(t, err, "expected non-zero exit, got:\n%s", out)
	assert.Contains(t, out, "not found")
}
