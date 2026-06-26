package server

import (
	"errors"
	"sync"
	"sync/atomic"
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

// TestReserveSerializesDuplicateName fires N concurrent reserve calls with the
// same dockerName and asserts exactly one wins. The pre-fix code did
// getByRef-then-put without a single lock acquisition; two threads could both
// see "no conflict" and both insert.
func TestReserveSerializesDuplicateName(t *testing.T) {
	s := newPendingStoreWith(time.Hour, time.Now)

	const concurrency = 32
	var wins int32
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := range concurrency {
		go func() {
			defer wg.Done()
			entry := &pendingContainer{
				ID:         "id-" + idSuffix(i),
				DockerName: "shared",
				Spec:       &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "shared-" + idSuffix(i)}},
				CreatedAt:  time.Now(),
				startCh:    make(chan struct{}),
			}
			if s.reserve(entry, "shared") {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins, "exactly one of %d concurrent reservations must win", concurrency)
}

func idSuffix(i int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[i&0xf], hex[(i>>4)&0xf]})
}
