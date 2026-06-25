//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDockerVersion is the smallest possible integration test: spin up the
// daemon on a UNIX socket, run `docker -H unix://... version`, confirm our
// advertised API version round-trips through the CLI.
//
// More tests (run / ps / logs / rm) are added incrementally as each layer is
// proven to work end-to-end.
func TestDockerVersion(t *testing.T) {
	env := newEnv(t)

	out, err := env.docker(t, 15*time.Second, "version")
	if err != nil {
		t.Fatalf("docker version failed: %v\nOutput:\n%s", err, out)
	}
	assert.Contains(t, out, "1.43", "expected API version 1.43 in output:\n%s", out)
}

// TestPing verifies the daemon answers /_ping without going through docker CLI.
// Catches socket-binding regressions independently of the docker CLI being
// installed or its version quirks.
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
