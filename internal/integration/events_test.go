//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `docker events --until=0` exits as soon as it sees the daemon close the
// stream. We're verifying the CLI<->daemon contract; hold-open semantics
// are covered at unit level.
func TestDockerEventsExits(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 10*time.Second, "events", "--since=0", "--until=0")
	require.NoError(t, err, "docker events failed:\n%s", out)
	assert.Empty(t, strings.TrimSpace(out), "we don't emit events yet")
}
