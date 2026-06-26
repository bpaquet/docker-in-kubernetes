//go:build integration

package integration_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compiles internal/integration/testcontainers_app and runs the binary as a
// subprocess against the daemon. The app lives in its own go.mod so
// testcontainers + transitives stay out of the production binary; this test
// just owns building it and asserting on the exit code.
func TestTestcontainersRedisRoundTrip(t *testing.T) {
	env := newEnv(t)

	binPath := buildTestcontainersApp(t)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = append(cmd.Environ(),
		"DOCKER_HOST=unix://"+env.SocketPath,
		"TESTCONTAINERS_RYUK_DISABLED=true",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "testcontainers_app failed:\n%s", out)
	assert.Contains(t, string(out), "OK", "expected the app's success marker")
}

func buildTestcontainersApp(t *testing.T) string {
	t.Helper()
	// Locate the app dir relative to this source file, not cwd — `go test`
	// invoked from a different directory shouldn't break the build path.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller(0) failed")
	appDir := filepath.Join(filepath.Dir(thisFile), "testcontainers_app")
	binPath := filepath.Join(t.TempDir(), "testcontainers_app")

	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = appDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build failed:\n%s", strings.TrimSpace(string(out)))
	return binPath
}
