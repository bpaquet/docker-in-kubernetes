//go:build integration

package integration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `docker events --since=0 --until=...` opens a /events stream; with a
// short --until the CLI exits 0 once that window passes.
func TestDockerEventsStreams(t *testing.T) {
	env := newEnv(t)

	start := time.Now()
	out, err := env.docker(t, 10*time.Second, "events", "--since=0", "--until=1s")
	require.NoError(t, err, "docker events failed:\n%s", out)
	assert.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond, "events should hold the stream open until --until")
}
