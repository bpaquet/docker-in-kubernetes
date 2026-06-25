package forwarder

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// SPDYForwarder tunnels via the apiserver's portforward subresource.
type SPDYForwarder struct {
	Clientset kubernetes.Interface
	RESTCfg   *rest.Config
	Logger    *slog.Logger
}

// NewSPDYForwarder returns an SPDYForwarder.
func NewSPDYForwarder(cs kubernetes.Interface, cfg *rest.Config, logger *slog.Logger) *SPDYForwarder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SPDYForwarder{Clientset: cs, RESTCfg: cfg, Logger: logger}
}

// Open opens 127.0.0.1:HostPort -> pod:ContainerPort tunnels.
func (f *SPDYForwarder) Open(ctx context.Context, namespace, pod string, mappings []Mapping) (Handle, error) {
	ports := make([]string, 0, len(mappings))
	for _, m := range mappings {
		if m.HostPort == 0 {
			continue
		}
		ports = append(ports, fmt.Sprintf("%d:%d", m.HostPort, m.ContainerPort))
	}
	if len(ports) == 0 {
		return &spdyHandle{}, nil
	}

	roundTripper, upgrader, err := spdy.RoundTripperFor(f.RESTCfg)
	if err != nil {
		return nil, fmt.Errorf("spdy round tripper: %w", err)
	}

	req := f.Clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, "POST", req.URL())

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.NewOnAddresses(
		dialer,
		[]string{"127.0.0.1"},
		ports,
		stopCh,
		readyCh,
		io.Discard,
		newSlogWriter(f.Logger, slog.LevelWarn),
	)
	if err != nil {
		return nil, fmt.Errorf("new port forwarder: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-readyCh:
	case err := <-errCh:
		return nil, fmt.Errorf("port forward setup: %w", err)
	case <-ctx.Done():
		close(stopCh)
		return nil, ctx.Err()
	}

	return &spdyHandle{stop: stopCh}, nil
}

type spdyHandle struct {
	mu       sync.Mutex
	stop     chan struct{}
	isClosed bool
}

// Close is idempotent.
func (h *spdyHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.isClosed {
		return nil
	}
	h.isClosed = true
	if h.stop != nil {
		close(h.stop)
	}
	return nil
}

// slogWriter sinks the portforward package's stdout/stderr writes into slog.
type slogWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func newSlogWriter(logger *slog.Logger, level slog.Level) io.Writer {
	return &slogWriter{logger: logger, level: level}
}

func (s *slogWriter) Write(p []byte) (int, error) {
	s.logger.Log(context.Background(), s.level, "spdy: "+stripTrailingNewline(string(p)))
	return len(p), nil
}

func stripTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
