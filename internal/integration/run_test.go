//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerRunWithoutDetachFailsFast(t *testing.T) {
	env := newEnv(t)
	name := "it-noattach-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	out, err := env.docker(t, 15*time.Second, "run", "--name", name, "alpine:3", "echo", "hi")
	require.Error(t, err, "expected non-zero exit; output:\n%s", out)
	assert.Contains(t, strings.ToLower(out), "use -d", "expected our 'use -d' guidance in output:\n%s", out)
}
