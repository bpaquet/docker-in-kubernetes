package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// PodIPResolver looks up a Pod's IP.
type PodIPResolver interface {
	PodIP(ctx context.Context, namespace, pod string) (string, error)
}

// Dialer is satisfied by *net.Dialer; the interface lets tests substitute fakes.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type netDialer struct{ d net.Dialer }

// DialContext satisfies Dialer.
func (n netDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.d.DialContext(ctx, network, address)
}

// TCPForwarder dials Pod IPs directly. Used in in-cluster mode.
type TCPForwarder struct {
	Resolver PodIPResolver
	Dialer   Dialer
	Logger   *slog.Logger
}

// NewTCPForwarder returns a TCPForwarder backed by net.Dialer.
func NewTCPForwarder(resolver PodIPResolver, logger *slog.Logger) *TCPForwarder {
	if logger == nil {
		logger = slog.Default()
	}
	return &TCPForwarder{
		Resolver: resolver,
		Dialer:   netDialer{d: net.Dialer{Timeout: 5 * time.Second}},
		Logger:   logger,
	}
}

// Open opens 127.0.0.1:HostPort -> podIP:ContainerPort proxies.
func (f *TCPForwarder) Open(ctx context.Context, namespace, pod string, mappings []Mapping) (Handle, error) {
	podIP, err := f.Resolver.PodIP(ctx, namespace, pod)
	if err != nil {
		return nil, fmt.Errorf("resolve pod ip: %w", err)
	}
	if podIP == "" {
		return nil, fmt.Errorf("pod %s/%s has no PodIP", namespace, pod)
	}

	h := &tcpHandle{logger: f.Logger}
	for _, m := range mappings {
		if m.HostPort == 0 {
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(int(m.HostPort)))
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("listen 127.0.0.1:%d: %w", m.HostPort, err)
		}
		target := net.JoinHostPort(podIP, strconv.Itoa(int(m.ContainerPort)))
		h.add(ln)
		go f.acceptLoop(ln, target, h)
	}
	return h, nil
}

func (f *TCPForwarder) acceptLoop(ln net.Listener, target string, h *tcpHandle) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if h.closed() {
				return
			}
			f.Logger.Warn("forwarder accept error", "addr", ln.Addr(), "err", err)
			return
		}
		go f.proxy(conn, target)
	}
}

func (f *TCPForwarder) proxy(client net.Conn, target string) {
	defer func() { _ = client.Close() }()
	upstream, err := f.Dialer.DialContext(context.Background(), "tcp", target)
	if err != nil {
		f.Logger.Warn("forwarder upstream dial failed", "target", target, "err", err)
		return
	}
	defer func() { _ = upstream.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, client) }()
	go func() { defer wg.Done(); _, _ = io.Copy(client, upstream) }()
	wg.Wait()
}

type tcpHandle struct {
	mu        sync.Mutex
	listeners []net.Listener
	isClosed  bool
	logger    *slog.Logger
}

func (h *tcpHandle) add(ln net.Listener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, ln)
}

func (h *tcpHandle) closed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.isClosed
}

// Close is idempotent.
func (h *tcpHandle) Close() error {
	h.mu.Lock()
	if h.isClosed {
		h.mu.Unlock()
		return nil
	}
	h.isClosed = true
	lns := h.listeners
	h.listeners = nil
	h.mu.Unlock()

	var errs []error
	for _, ln := range lns {
		if err := ln.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
