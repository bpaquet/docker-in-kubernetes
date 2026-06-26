package server

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

// pendingContainer is a /create'd container that hasn't been /start'd yet.
// k8s has no "created but not running" pod state, so we stash the spec in
// memory until /start realizes it.
type pendingContainer struct {
	ID         string
	DockerName string
	Spec       *corev1.Pod
	Mappings   []podspec.PortMapping
	CreatedAt  time.Time

	mu      sync.Mutex
	started bool
	failed  error
	startCh chan struct{}
}

func (p *pendingContainer) markStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.failed != nil {
		return
	}
	p.started = true
	close(p.startCh)
}

func (p *pendingContainer) markFailed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.failed != nil {
		return
	}
	p.failed = err
	close(p.startCh)
}

// waitForStart blocks until markStarted or markFailed fires, or ctx cancels.
// Returns the markFailed error, or ctx.Err, or nil on successful start.
func (p *pendingContainer) waitForStart(ctx context.Context) error {
	select {
	case <-p.startCh:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.failed
	case <-ctx.Done():
		return ctx.Err()
	}
}

type pendingStore struct {
	mu sync.Mutex
	m  map[string]*pendingContainer
}

func newPendingStore() *pendingStore {
	return &pendingStore{m: make(map[string]*pendingContainer)}
}

func (s *pendingStore) put(p *pendingContainer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.ID] = p
}

// getByRef resolves a CLI reference (full ID, short ID, pod name, --name).
func (s *pendingStore) getByRef(ref string) *pendingContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.m[ref]; ok {
		return p
	}
	for id, p := range s.m {
		if podspec.ShortID(id) == ref || p.Spec.Name == ref || p.DockerName == ref {
			return p
		}
	}
	return nil
}

func (s *pendingStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

func (s *pendingStore) list() []*pendingContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pendingContainer, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	return out
}

var errPendingRemoved = errors.New("pending container removed before start")
