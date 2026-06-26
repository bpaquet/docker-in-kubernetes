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
	mu  sync.Mutex
	m   map[string]*pendingContainer
	ttl time.Duration
	now func() time.Time
}

func newPendingStore() *pendingStore {
	return newPendingStoreWith(pendingTTL, time.Now)
}

func newPendingStoreWith(ttl time.Duration, now func() time.Time) *pendingStore {
	s := &pendingStore{m: make(map[string]*pendingContainer), ttl: ttl, now: now}
	return s
}

// pendingTTL caps how long a /create'd-but-never-/start'ed entry lingers.
// CLIs that crash between /create and /start would otherwise leave phantoms.
const pendingTTL = time.Hour

func (s *pendingStore) put(p *pendingContainer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.ID] = p
}

// reserve atomically claims a name in the pending store. Returns true if p
// was inserted; false if a pending entry already exists for the same ID or
// (when non-empty) the same dockerName. Callers must use this — not the
// getByRef + put pair — to close the TOCTOU window between two concurrent
// /create calls racing on the same --name.
func (s *pendingStore) reserve(p *pendingContainer, dockerName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	if dockerName != "" && s.findLocked(dockerName) != nil {
		return false
	}
	if _, exists := s.m[p.ID]; exists {
		return false
	}
	s.m[p.ID] = p
	return true
}

// getByRef resolves a CLI reference (full ID, short ID, pod name, --name).
func (s *pendingStore) getByRef(ref string) *pendingContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	return s.findLocked(ref)
}

func (s *pendingStore) findLocked(ref string) *pendingContainer {
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

// reapLocked drops entries older than ttl that nothing started. Marks them
// failed so any /attach or /wait waiters unblock.
func (s *pendingStore) reapLocked() {
	if s.ttl <= 0 {
		return
	}
	cutoff := s.now().Add(-s.ttl)
	for id, p := range s.m {
		if p.CreatedAt.Before(cutoff) {
			p.markFailed(errPendingExpired)
			delete(s.m, id)
		}
	}
}

func (s *pendingStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

func (s *pendingStore) list() []*pendingContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	out := make([]*pendingContainer, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	return out
}

var (
	errPendingRemoved = errors.New("pending container removed before start")
	errPendingExpired = errors.New("pending container expired before start")
)
