// Package forwarder bridges localhost ports on the daemon host to container
// ports on Pods. Two backends:
//
//   - SPDY:  apiserver port-forward (local mode, daemon on a laptop)
//   - TCP:   direct dial to PodIP   (in-cluster mode, daemon is a Pod itself)
//
// Choose at startup with PickBackend based on os.Getenv("KUBERNETES_SERVICE_HOST").
package forwarder

import (
	"context"
	"errors"
	"sync"
)

// Mapping is one host -> container port pair. Both are 16-bit unsigned.
type Mapping struct {
	HostPort      uint16
	ContainerPort uint16
}

// Handle owns the goroutines and listeners for one Open() call. Close stops
// all of them and is idempotent.
type Handle interface {
	Close() error
}

// Forwarder is the contract both backends satisfy.
type Forwarder interface {
	Open(ctx context.Context, namespace, pod string, mappings []Mapping) (Handle, error)
}

// Registry tracks open Handles by container ID so the HTTP layer can close
// them on rm/kill/stop without holding a per-request goroutine map.
type Registry struct {
	mu      sync.Mutex
	handles map[string]Handle
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{handles: make(map[string]Handle)}
}

// Set replaces the handle bound to id, closing any existing one.
func (r *Registry) Set(id string, h Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.handles[id]; ok {
		_ = prev.Close()
	}
	r.handles[id] = h
}

// Close stops and removes the handle bound to id. No-op if absent.
func (r *Registry) Close(id string) error {
	r.mu.Lock()
	h, ok := r.handles[id]
	delete(r.handles, id)
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return h.Close()
}

// Has reports whether a forwarder is registered for id.
func (r *Registry) Has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.handles[id]
	return ok
}

// closeAll drains the registry; closes returned through errors.Join so partial
// failures aren't hidden.
func (r *Registry) closeAll() error {
	r.mu.Lock()
	prev := r.handles
	r.handles = make(map[string]Handle)
	r.mu.Unlock()

	var errs []error
	for _, h := range prev {
		if err := h.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Shutdown closes every tracked Handle.
func (r *Registry) Shutdown() error { return r.closeAll() }
