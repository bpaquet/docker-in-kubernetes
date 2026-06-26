//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// End-to-end: drive testcontainers-go against our daemon. Spins up Redis,
// uses Endpoint() to discover host:port (our port-forwarder), and exercises
// PING/SET/GET via the real redis client.
//
// Ryuk is disabled per Design.md — the reaper container would try to dial
// the docker socket from inside the cluster, which can't reach the laptop.
func TestTestcontainersRedisRoundTrip(t *testing.T) {
	env := newEnv(t)
	t.Setenv("DOCKER_HOST", "unix://"+env.SocketPath)
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "testcontainers redis.Run")
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(container)
	})

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err, "container.Endpoint")

	client := redis.NewClient(&redis.Options{Addr: endpoint})
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Ping(ctx).Err(), "PING")
	require.NoError(t, client.Set(ctx, "k", "v", 0).Err(), "SET")
	got, err := client.Get(ctx, "k").Result()
	require.NoError(t, err, "GET")
	assert.Equal(t, "v", got)
}
