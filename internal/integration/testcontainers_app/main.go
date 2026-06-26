// Standalone testcontainers smoke test. Reads DOCKER_HOST from the env and
// drives `redis:7-alpine` via testcontainers-go + go-redis. Exits 0 on
// successful PING/SET/GET round-trip; non-zero with a message otherwise.
//
// Lives in its own go.mod so testcontainers + transitives don't pollute the
// daemon's dependency tree. Compiled and run as a subprocess by
// internal/integration/testcontainers_test.go.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		return fmt.Errorf("testcontainers redis.Run: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(container) }()

	endpoint, err := container.Endpoint(ctx, "")
	if err != nil {
		return fmt.Errorf("container.Endpoint: %w", err)
	}
	fmt.Println("endpoint:", endpoint)

	client := redis.NewClient(&redis.Options{Addr: endpoint})
	defer func() { _ = client.Close() }()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("PING: %w", err)
	}
	if err := client.Set(ctx, "k", "v", 0).Err(); err != nil {
		return fmt.Errorf("SET: %w", err)
	}
	got, err := client.Get(ctx, "k").Result()
	if err != nil {
		return fmt.Errorf("GET: %w", err)
	}
	if got != "v" {
		return fmt.Errorf("GET returned %q, want %q", got, "v")
	}
	return nil
}
