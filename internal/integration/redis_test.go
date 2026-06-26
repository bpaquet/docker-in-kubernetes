//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDockerVersion(t *testing.T) {
	env := newEnv(t)

	out, err := env.docker(t, 15*time.Second, "version")
	if err != nil {
		t.Fatalf("docker version failed: %v\nOutput:\n%s", err, out)
	}
	assert.Contains(t, out, "1.43", "expected API version 1.43 in output:\n%s", out)
}

// Direct /_ping over the socket, no docker CLI involved.
func TestPing(t *testing.T) {
	env := newEnv(t)

	conn, err := dialSocket(env.SocketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("GET /_ping HTTP/1.1\r\nHost: docker\r\n\r\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	assert.Contains(t, got, "HTTP/1.1 200")
	assert.Contains(t, strings.ToUpper(got), "API-VERSION: 1.43")
	assert.Contains(t, got, "OK")
}
