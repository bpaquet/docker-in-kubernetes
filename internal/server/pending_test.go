package server

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPendingStoreReapsExpiredEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newPendingStoreWith(time.Hour, func() time.Time { return now })

	old := &pendingContainer{
		ID:        "old",
		Spec:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old"}},
		CreatedAt: now.Add(-2 * time.Hour),
		startCh:   make(chan struct{}),
	}
	fresh := &pendingContainer{
		ID:        "fresh",
		Spec:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "fresh"}},
		CreatedAt: now.Add(-5 * time.Minute),
		startCh:   make(chan struct{}),
	}
	s.put(old)
	s.put(fresh)

	assert.Nil(t, s.getByRef("old"), "expired entry should be reaped on access")
	assert.NotNil(t, s.getByRef("fresh"), "fresh entry should still be present")

	select {
	case <-old.startCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expired entry's startCh should be closed by reaper")
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	assert.True(t, errors.Is(old.failed, errPendingExpired), "expired waiter should see errPendingExpired")
}
