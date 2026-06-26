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

// docker run -d --name X alpine:3 echo a b c d → stdout has "a b c d".
func TestCreateCmdMultipleArgs(t *testing.T) {
	env := newEnv(t)
	name := "it-margs-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name, "alpine:3",
		"echo", "alpha", "beta", "gamma",
	)
	require.NoError(t, err, "docker run output:\n%s", out)
	requireLogsContain(t, env, name, "alpha beta gamma")
}

// docker run -d alpine sh -c 'echo "a b c" && date' — shell special chars pass through.
func TestCreateCmdShellSpecialChars(t *testing.T) {
	env := newEnv(t)
	name := "it-shch-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name, "alpine:3",
		"sh", "-c", `echo "with spaces & ampersand" && echo done`,
	)
	require.NoError(t, err, "docker run output:\n%s", out)
	requireLogsContain(t, env, name, "with spaces & ampersand")
}

// alpine has no default ENTRYPOINT; we run nginx:alpine, whose default
// command writes a known startup line. Confirms the image's own CMD path
// works when the user passes no override.
func TestCreateImageDefaultEntrypoint(t *testing.T) {
	env := newEnv(t)
	name := "it-defaults-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 90*time.Second, "run", "-d", "--name", name, "redis:7-alpine")
	require.NoError(t, err, "docker run output:\n%s", out)
	requireLogsContain(t, env, name, "Ready to accept connections")
}

func TestCreateCmdExitsNonZero(t *testing.T) {
	env := newEnv(t)
	name := "it-fail-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 90*time.Second,
		"run", "-d", "--name", name, "alpine:3",
		"sh", "-c", "exit 7",
	)
	// With our /create that waits for Ready, a fast-exiting container will
	// race the readiness check; either path is acceptable as long as docker
	// run reports the failure.
	if err == nil {
		// Container created; verify wait reports the exit code.
		waitOut, _ := env.docker(t, 15*time.Second, "wait", name)
		assert.Contains(t, waitOut, "7", "docker wait should report exit 7; got:\n%s", waitOut)
	} else {
		assert.NotEmpty(t, out)
	}
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

// docker run --memory --cpus --user: pod spec gets matching resources and
// security context; `id -u` confirms the runtime uid.
func TestCreateResourcesAndUser(t *testing.T) {
	env := newEnv(t)
	name := "it-res-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 60*time.Second,
		"run", "-d", "--name", name,
		"--memory", "64m",
		"--cpus", "0.25",
		"--user", "1000:1001",
		"alpine:3",
		"sh", "-c", "id -u; id -g; sleep 60",
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	pod, err := env.Pods.Get(t.Context(), name)
	require.NoError(t, err)
	require.Len(t, pod.Spec.Containers, 1)
	c := pod.Spec.Containers[0]

	mem := c.Resources.Limits["memory"]
	cpu := c.Resources.Limits["cpu"]
	assert.Equal(t, "64Mi", mem.String())
	assert.Equal(t, "250m", cpu.String())

	memReq := c.Resources.Requests["memory"]
	cpuReq := c.Resources.Requests["cpu"]
	assert.Equal(t, mem.String(), memReq.String())
	assert.Equal(t, cpu.String(), cpuReq.String())

	require.NotNil(t, c.SecurityContext)
	require.NotNil(t, c.SecurityContext.RunAsUser)
	require.NotNil(t, c.SecurityContext.RunAsGroup)
	assert.Equal(t, int64(1000), *c.SecurityContext.RunAsUser)
	assert.Equal(t, int64(1001), *c.SecurityContext.RunAsGroup)

	requireLogsContain(t, env, name, "1000")
	requireLogsContain(t, env, name, "1001")

	inspectOut, err := env.docker(t, 15*time.Second, "inspect", name)
	require.NoError(t, err)
	assert.Contains(t, inspectOut, `"User": "1000:1001"`)
	assert.Contains(t, inspectOut, `"Memory": 67108864`)
}

func TestCreateRejectsNonNumericUser(t *testing.T) {
	env := newEnv(t)
	name := "it-baduser-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 30*time.Second,
		"run", "-d", "--name", name,
		"--user", "root",
		"alpine:3", "sleep", "60",
	)
	require.Error(t, err, "expected non-numeric --user to be rejected; got:\n%s", out)
	assert.Contains(t, strings.ToLower(out), "numeric uid")
}
