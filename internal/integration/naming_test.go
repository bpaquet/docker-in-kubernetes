//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docker run without --name uses the generated dik-<image>-<hex6> path.
func TestDockerRunGeneratedName(t *testing.T) {
	env := newEnv(t)

	out, err := env.docker(t, 60*time.Second, "run", "-d", "alpine:3", "sleep", "60")
	require.NoError(t, err, "docker run output:\n%s", out)
	id := strings.TrimSpace(strings.Split(out, "\n")[0])
	require.NotEmpty(t, id)
	t.Cleanup(func() { _, _ = env.docker(t, 15*time.Second, "rm", "-f", id) })

	psOut, err := env.docker(t, 15*time.Second, "ps")
	require.NoError(t, err)
	assert.Contains(t, psOut, "dik-alpine-", "ps NAMES column should show the generated name")
}

func TestDockerRunDuplicateNameFails(t *testing.T) {
	env := newEnv(t)
	name := "it-dup-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second, "run", "-d", "--name", name, "alpine:3", "sleep", "60")
	require.NoError(t, err, "first run should succeed; output:\n%s", out)

	out, err = env.docker(t, 30*time.Second, "run", "-d", "--name", name, "alpine:3", "sleep", "60")
	require.Error(t, err, "second run with same name should fail; output:\n%s", out)
	assert.Contains(t, strings.ToLower(out), "already exists", "expected duplicate-name error; got:\n%s", out)
}

func TestDockerRunWithLabels(t *testing.T) {
	env := newEnv(t)
	name := "it-lbl-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"--label", "dik.test/key=value",
		"--label", "dik.test/other=present",
		"alpine:3", "sleep", "60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	inspectOut, err := env.docker(t, 15*time.Second, "inspect", name)
	require.NoError(t, err)
	assert.Contains(t, inspectOut, `"dik.test/key": "value"`)
	assert.Contains(t, inspectOut, `"dik.test/other": "present"`)
}
