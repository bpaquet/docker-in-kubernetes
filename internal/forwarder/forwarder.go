// Package forwarder bridges host ports to Pod ports (SPDY local, TCP in-cluster).
package forwarder

import (
	"context"
	"errors"
	"sync"
)

// Mapping is one host -> container port pair.
type Mapping struct {
	HostPort      uint16
	ContainerPort uint16
}

// Handle stops every listener/goroutine opened by one Open call. Idempotent.
type Handle interface {
	Close() error
}

// Forwarder is the SPDY/TCP backend interface.
type Forwarder interface {
	Open(ctx context.Context, namespace, pod string, mappings []Mapping) (Handle, error)
}

// Registry tracks open Handles by container ID.
type Registry struct {
	mu      sync.Mutex
	handles map[string]Handle
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{handles: make(map[string]Handle)}
}

// Set binds h to id, closing any previously-bound handle.
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

// Shutdown closes every tracked Handle. Errors are joined.
func (r *Registry) Shutdown() error {
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
