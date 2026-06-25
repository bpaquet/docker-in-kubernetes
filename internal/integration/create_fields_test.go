//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateEnvVar(t *testing.T) {
	env := newEnv(t)
	name := "it-env-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	const marker = "dik-env-value"
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"-e", "DIK_MARKER="+marker,
		"alpine:3",
		"sh", "-c", "echo dik:$DIK_MARKER; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	requireLogsContain(t, env, name, "dik:"+marker)
}

func TestCreateCmd(t *testing.T) {
	env := newEnv(t)
	name := "it-cmd-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	const marker = "dik-cmd-arg"
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"alpine:3",
		"sh", "-c", "echo "+marker+"; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	requireLogsContain(t, env, name, marker)
}

func TestCreateEntrypoint(t *testing.T) {
	env := newEnv(t)
	name := "it-ep-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	const marker = "dik-entrypoint-message"
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"--entrypoint", "sh",
		"alpine:3",
		"-c", "echo "+marker+"; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	requireLogsContain(t, env, name, marker)
}

func TestCreateWorkingDir(t *testing.T) {
	env := newEnv(t)
	name := "it-wd-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	const dir = "/tmp"
	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"-w", dir,
		"alpine:3",
		"sh", "-c", "pwd; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	requireLogsContain(t, env, name, dir)
}

func TestCreateEntrypointMissingBinaryFails(t *testing.T) {
	env := newEnv(t)
	name := "it-bad-ep-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	start := time.Now()
	out, err := env.docker(t, 90*time.Second,
		"run", "-d", "--name", name,
		"--entrypoint", "/usr/local/bin/no-such-binary-dik",
		"alpine:3",
	)
	elapsed := time.Since(start)
	require.Error(t, err, "expected non-zero exit; output:\n%s", out)
	assert.Less(t, elapsed, 90*time.Second, "should fail well before the 90s docker timeout")
}

// requireLogsContain polls `docker logs` until want appears (or we give up).
func requireLogsContain(t *testing.T, env *testEnv, id, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := env.docker(t, 10*time.Second, "logs", id)
		if err != nil {
			t.Logf("docker logs transient error: %v\n%s", err, out)
			return false
		}
		return strings.Contains(out, want)
	}, 30*time.Second, 500*time.Millisecond, "docker logs should contain %q for %s", want, id)
}
