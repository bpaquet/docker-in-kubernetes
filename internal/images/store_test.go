package images_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/images"
)

func TestStoreRecordAndList(t *testing.T) {
	s := images.New()
	t0 := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)

	s.Record("redis:alpine", "alpine", t0)
	s.Record("postgres:16", "16", t0.Add(time.Second))

	list := s.List()
	require.Len(t, list, 2)
	assert.Equal(t, "postgres:16", list[0].Ref)
	assert.Equal(t, "redis:alpine", list[1].Ref)
	assert.True(t, s.Has("redis:alpine"))
	assert.False(t, s.Has("unknown"))
}

func TestStoreRecordUpserts(t *testing.T) {
	s := images.New()
	t0 := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)

	s.Record("redis:alpine", "alpine", t0)
	s.Record("redis:alpine", "alpine", t0.Add(time.Hour))

	list := s.List()
	require.Len(t, list, 1)
	assert.Equal(t, t0.Add(time.Hour), list[0].PulledAt)
}

func TestRecordID(t *testing.T) {
	r := images.Record{Ref: "redis:alpine"}
	id := r.ID()
	// sha256("redis:alpine") deterministic
	assert.Len(t, id, len("sha256:")+64)
	assert.Equal(t, id, images.Record{Ref: "redis:alpine"}.ID())
	assert.NotEqual(t, id, images.Record{Ref: "redis:7"}.ID())
}

func TestStoreFind(t *testing.T) {
	s := images.New()
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	s.Record("redis:latest", "latest", now)
	s.Record("postgres:16", "16", now)
	redisID := images.Record{Ref: "redis:latest"}.ID()

	tests := []struct {
		name    string
		lookup  string
		wantRef string
		wantOK  bool
	}{
		{"exact ref", "redis:latest", "redis:latest", true},
		{"untagged falls back to :latest", "redis", "redis:latest", true},
		{"unknown", "nope", "", false},
		{"id prefix", redisID[7:19], "redis:latest", true},
		{"id full", redisID, "redis:latest", true},
		{"short prefix rejected", redisID[7:9], "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := s.Find(tc.lookup)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantRef, r.Ref)
			}
		})
	}
}

// docker compose pulls with the normalized name and inspects with the short
// one. Canonicalization must hide that asymmetry.
func TestStoreCanonicalizesDockerHubRefs(t *testing.T) {
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		recordRef string
		lookups   []string
	}{
		{"docker.io/library/x ↔ x", "docker.io/library/redis:latest", []string{"redis", "redis:latest", "docker.io/library/redis", "library/redis"}},
		{"library/x ↔ x", "library/postgres:16", []string{"postgres:16", "library/postgres:16", "docker.io/library/postgres:16"}},
		{"docker.io/user/x ↔ user/x", "docker.io/grafana/grafana:11", []string{"grafana/grafana:11", "docker.io/grafana/grafana:11"}},
		{"index.docker.io alias", "index.docker.io/library/nginx:latest", []string{"nginx", "nginx:latest", "docker.io/library/nginx:latest"}},
		{"digest under docker.io/library", "docker.io/library/redis@sha256:deadbeef", []string{"redis@sha256:deadbeef", "library/redis@sha256:deadbeef"}},
		{"non-hub registry untouched", "ghcr.io/foo/bar:1", []string{"ghcr.io/foo/bar:1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := images.New()
			s.Record(tc.recordRef, "", now)
			for _, lookup := range tc.lookups {
				_, ok := s.Find(lookup)
				assert.True(t, ok, "Find(%q) should hit", lookup)
			}
		})
	}
}

// Pull as full ref, remove as short — must succeed.
func TestStoreRemoveCanonical(t *testing.T) {
	s := images.New()
	s.Record("docker.io/library/redis:latest", "latest", time.Now())
	r, ok := s.Remove("redis")
	require.True(t, ok)
	assert.Equal(t, "redis:latest", r.Ref)
	assert.False(t, s.Has("docker.io/library/redis:latest"))
}

func TestStoreRemove(t *testing.T) {
	s := images.New()
	s.Record("redis:alpine", "alpine", time.Now())
	s.Record("postgres:16", "16", time.Now())

	r, ok := s.Remove("redis:alpine")
	require.True(t, ok)
	assert.Equal(t, "redis:alpine", r.Ref)
	assert.False(t, s.Has("redis:alpine"))
	assert.True(t, s.Has("postgres:16"))

	_, ok = s.Remove("redis:alpine")
	assert.False(t, ok)
}
