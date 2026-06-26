package networks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/networks"
)

func TestStoreRecordAndFind(t *testing.T) {
	s := networks.New()
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)

	s.Record("bpa_default", "", map[string]string{"com.docker.compose.project": "bpa"}, now)

	r, ok := s.Find("bpa_default")
	require.True(t, ok)
	assert.Equal(t, "bpa_default", r.Name)
	assert.Equal(t, "bridge", r.Driver, "empty driver should default to bridge")
	assert.Equal(t, "bpa", r.Labels["com.docker.compose.project"])
	assert.Len(t, r.ID(), 64)
}

func TestStoreSeedsBridge(t *testing.T) {
	s := networks.New()
	r, ok := s.Find("bridge")
	require.True(t, ok)
	assert.Equal(t, "bridge", r.Driver)
}

func TestStoreList(t *testing.T) {
	s := networks.New()
	t0 := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	s.Record("a", "bridge", nil, t0)
	s.Record("b", "bridge", nil, t0.Add(time.Second))

	list := s.List()
	require.Len(t, list, 3, "bridge + a + b")
	// newest first; bridge has unix-epoch CreatedAt so it sorts last.
	assert.Equal(t, "b", list[0].Name)
	assert.Equal(t, "a", list[1].Name)
	assert.Equal(t, "bridge", list[2].Name)
}

func TestStoreRemove(t *testing.T) {
	s := networks.New()
	s.Record("a", "bridge", nil, time.Now())

	r, ok := s.Remove("a")
	require.True(t, ok)
	assert.Equal(t, "a", r.Name)

	_, ok = s.Find("a")
	assert.False(t, ok)

	_, ok = s.Remove("a")
	assert.False(t, ok, "second remove is a miss")
}
