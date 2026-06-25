package forwarder_test

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
)

type stubHandle struct {
	closed atomic.Bool
}

func (s *stubHandle) Close() error {
	s.closed.Store(true)
	return nil
}

func TestRegistryCloseClosesHandle(t *testing.T) {
	r := forwarder.NewRegistry()
	h := &stubHandle{}
	r.Set("id1", h)
	assert.True(t, r.Has("id1"))

	assert.NoError(t, r.Close("id1"))
	assert.False(t, r.Has("id1"))
	assert.True(t, h.closed.Load())
}

func TestRegistryCloseMissingIsNoOp(t *testing.T) {
	r := forwarder.NewRegistry()
	assert.NoError(t, r.Close("nope"))
}

func TestRegistrySetReplacesAndClosesPrevious(t *testing.T) {
	r := forwarder.NewRegistry()
	a := &stubHandle{}
	b := &stubHandle{}
	r.Set("id", a)
	r.Set("id", b)
	assert.True(t, a.closed.Load())
	assert.False(t, b.closed.Load())
}

func TestRegistryShutdownClosesAll(t *testing.T) {
	r := forwarder.NewRegistry()
	h1, h2 := &stubHandle{}, &stubHandle{}
	r.Set("a", h1)
	r.Set("b", h2)

	assert.NoError(t, r.Shutdown())
	assert.True(t, h1.closed.Load())
	assert.True(t, h2.closed.Load())
	assert.False(t, r.Has("a"))
}
