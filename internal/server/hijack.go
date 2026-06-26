package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// hijack takes over the underlying TCP connection. NewResponseController walks
// any wrapping middleware (e.g. statusRecorder) via its Unwrap method.
func hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w).Hijack()
}

// writeRawStreamResponse sends the hijacked-stream response headers. When the
// client requested `Connection: Upgrade, tcp` (docker CLI default for
// attach/exec start), we reply 101 Upgraded so the CLI's TCP upgrade handshake
// completes; otherwise we send 200 OK.
func writeRawStreamResponse(r *http.Request, brw *bufio.ReadWriter) error {
	status := "HTTP/1.1 200 OK\r\n"
	upgrade := ""
	if strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		status = "HTTP/1.1 101 UPGRADED\r\n"
		upgrade = "Connection: Upgrade\r\nUpgrade: " + r.Header.Get("Upgrade") + "\r\n"
	}
	_, err := brw.WriteString(status +
		"Content-Type: application/vnd.docker.raw-stream\r\n" +
		"Api-Version: " + APIVersion + "\r\n" +
		"Server: docker-in-kubernetes\r\n" +
		upgrade +
		"\r\n")
	if err != nil {
		return err
	}
	return brw.Flush()
}

// framedWriter serializes writes from multiple goroutines into one underlying
// stream, each chunk prefixed with a stdcopy frame header (stream byte +
// length). Used for non-TTY attach/exec where stdout and stderr share one TCP
// connection.
type framedWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	stream byte
}

func (f *framedWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeMultiplexedChunk(f.w, f.stream, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// multiplexedStdoutStderr returns (stdout, stderr) writers that share mu and w.
// When TTY is true, both return the raw conn unwrapped — Docker uses a single
// stream in TTY mode.
func multiplexedStdoutStderr(w io.Writer, tty bool) (io.Writer, io.Writer) {
	if tty {
		return w, w
	}
	var mu sync.Mutex
	return &framedWriter{mu: &mu, w: w, stream: 1},
		&framedWriter{mu: &mu, w: w, stream: 2}
}
