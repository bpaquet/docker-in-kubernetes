// Package images is the in-memory record of `docker pull` calls.
//
// We do not actually pull anything — pulls happen in the cluster on pod
// create. The store exists so /images/create can pretend it pulled, and so
// /images/json, /images/{name}/json, and DELETE /images/{name} can report
// and remove what was asked for.
package images

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record is one pulled image.
type Record struct {
	Ref      string    // full ref the CLI asked for, e.g. "redis:alpine"
	Tag      string    // tag portion ("" for digest refs)
	PulledAt time.Time // last time we recorded a pull for Ref
}

// ID returns the synthetic image ID: "sha256:" + hex(sha256(ref)).
func (r Record) ID() string {
	sum := sha256.Sum256([]byte(r.Ref))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Store is the thread-safe in-memory registry of pulled refs.
type Store struct {
	mu sync.RWMutex
	m  map[string]Record
}

// New returns an empty Store.
func New() *Store {
	return &Store{m: make(map[string]Record)}
}

// Record upserts r keyed by canonical(ref) and returns the stored copy.
func (s *Store) Record(ref, tag string, now time.Time) Record {
	ref = canonicalRef(ref)
	r := Record{Ref: ref, Tag: tag, PulledAt: now.UTC()}
	s.mu.Lock()
	s.m[ref] = r
	s.mu.Unlock()
	return r
}

// canonicalRef strips Docker Hub's implicit prefixes so that
// `docker.io/library/redis`, `library/redis`, and `redis` all hash to `redis`.
// Why: docker compose normalizes refs on pull (`fromImage=docker.io/library/redis`)
// but inspects with the short form (`GET /images/redis/json`).
func canonicalRef(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "docker.io/library/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(ref, "docker.io/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(ref, "library/"); ok {
		return rest
	}
	return ref
}

// List returns all records, newest first.
func (s *Store) List() []Record {
	s.mu.RLock()
	out := make([]Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PulledAt.After(out[j].PulledAt) })
	return out
}

// Has reports whether ref has been pulled.
func (s *Store) Has(ref string) bool {
	ref = canonicalRef(ref)
	s.mu.RLock()
	_, ok := s.m[ref]
	s.mu.RUnlock()
	return ok
}

// Find resolves a CLI-style name to a record. Lookup order:
//  1. Exact ref match
//  2. "name" → "name:latest"
//  3. ID prefix match (with or without "sha256:" prefix), unique only
//
// Returns ok=false if nothing matches or the prefix is ambiguous.
func (s *Store) Find(name string) (Record, bool) {
	if name == "" {
		return Record{}, false
	}
	name = canonicalRef(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.m[name]; ok {
		return r, true
	}
	if !strings.ContainsAny(name, ":@") {
		if r, ok := s.m[name+":latest"]; ok {
			return r, true
		}
	}
	return findByIDPrefix(s.m, name)
}

// Remove deletes the record matching name (same lookup as Find) and returns it.
func (s *Store) Remove(name string) (Record, bool) {
	if name == "" {
		return Record{}, false
	}
	name = canonicalRef(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[name]; ok {
		delete(s.m, r.Ref)
		return r, true
	}
	if !strings.ContainsAny(name, ":@") {
		if r, ok := s.m[name+":latest"]; ok {
			delete(s.m, r.Ref)
			return r, true
		}
	}
	if r, ok := findByIDPrefix(s.m, name); ok {
		delete(s.m, r.Ref)
		return r, true
	}
	return Record{}, false
}

func findByIDPrefix(m map[string]Record, name string) (Record, bool) {
	prefix := strings.TrimPrefix(name, "sha256:")
	if len(prefix) < 4 || !isHex(prefix) {
		return Record{}, false
	}
	var match Record
	hits := 0
	for _, r := range m {
		if strings.HasPrefix(strings.TrimPrefix(r.ID(), "sha256:"), prefix) {
			match = r
			hits++
		}
	}
	if hits != 1 {
		return Record{}, false
	}
	return match, true
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
