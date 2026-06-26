// Package networks backs the /networks endpoints. The k8s namespace is the
// real fabric; this store only exists to round-trip compose's pre-create probe.
package networks

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Record is one network entry.
type Record struct {
	Name      string
	Driver    string
	Labels    map[string]string
	CreatedAt time.Time
}

// ID returns hex(sha256(name)) — matches Docker's network ID format (no sha256: prefix).
func (r Record) ID() string {
	sum := sha256.Sum256([]byte(r.Name))
	return hex.EncodeToString(sum[:])
}

// Store is the thread-safe in-memory registry.
type Store struct {
	mu sync.RWMutex
	m  map[string]Record
}

// New returns a Store pre-seeded with the `bridge` network so `docker network
// ls` matches what tools expect from a real daemon.
func New() *Store {
	s := &Store{m: make(map[string]Record)}
	s.Record("bridge", "bridge", nil, time.Unix(0, 0))
	return s
}

// Has reports whether name is recorded.
func (s *Store) Has(name string) bool {
	s.mu.RLock()
	_, ok := s.m[name]
	s.mu.RUnlock()
	return ok
}

// Record upserts by name and returns the stored copy.
func (s *Store) Record(name, driver string, labels map[string]string, now time.Time) Record {
	if driver == "" {
		driver = "bridge"
	}
	r := Record{Name: name, Driver: driver, Labels: copyLabels(labels), CreatedAt: now.UTC()}
	s.mu.Lock()
	s.m[name] = r
	s.mu.Unlock()
	return r
}

// Find returns the record for name, or ok=false.
func (s *Store) Find(name string) (Record, bool) {
	s.mu.RLock()
	r, ok := s.m[name]
	s.mu.RUnlock()
	return r, ok
}

// Remove deletes the record matching name and returns it.
func (s *Store) Remove(name string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[name]
	if !ok {
		return Record{}, false
	}
	delete(s.m, name)
	return r, true
}

// List returns all records, newest first.
func (s *Store) List() []Record {
	s.mu.RLock()
	out := make([]Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
