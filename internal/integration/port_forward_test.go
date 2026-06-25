//go:build integration

package integration_test

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Drives a non-standard container port and bidirectional payload through the forwarder.
func TestPortForwardCustomPortRoundTrip(t *testing.T) {
	env := newEnv(t)
	name := "it-fwd-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	hostPort := freeLocalPort(t)
	const containerPort = 12345

	out, err := env.docker(t, 90*time.Second,
		"run", "-d", "--name", name,
		"-p", strconv.Itoa(hostPort)+":"+strconv.Itoa(containerPort),
		"redis:7-alpine",
		"redis-server", "--port", strconv.Itoa(containerPort),
	)
	require.NoError(t, err, "docker run output:\n%s", out)

	require.NoError(t, redisSetGet(t, hostPort, "dik-key", "dik-value"))
}

// redisSetGet issues SET+GET via RESP and asserts the value round-trips.
func redisSetGet(t *testing.T, hostPort int, key, value string) error {
	t.Helper()
	addr := "127.0.0.1:" + strconv.Itoa(hostPort)

	var conn net.Conn
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(respCommand("SET", key, value))); err != nil {
		return err
	}
	if err := expectPrefix(conn, "+OK\r\n"); err != nil {
		return err
	}

	if _, err := conn.Write([]byte(respCommand("GET", key))); err != nil {
		return err
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	reply := string(buf[:n])
	assert.Contains(t, reply, value, "expected GET to echo back the value; raw reply: %q", reply)
	return nil
}

func respCommand(args ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return b.String()
}

func expectPrefix(conn net.Conn, want string) error {
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != want {
		return fmt.Errorf("expected prefix %q, got %q", want, string(buf))
	}
	return nil
}
