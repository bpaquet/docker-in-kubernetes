package k8s_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

const testNS = "test-ns"

func managedPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels:    map[string]string{podspec.LabelManaged: "true"},
		},
	}
}

func newStore(t *testing.T, objs ...corev1.Pod) (*k8s.Pods, *fake.Clientset) {
	t.Helper()
	items := make([]runtime.Object, 0, len(objs))
	for i := range objs {
		items = append(items, &objs[i])
	}
	cs := fake.NewClientset(items...)
	store := k8s.NewPods(cs, testNS)
	store.SetPollInterval(5 * time.Millisecond)
	store.SetReadyTimeout(200 * time.Millisecond)
	return store, cs
}

func TestPodsCreateGetList(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	created, err := store.Create(ctx, managedPod("p1"))
	require.NoError(t, err)
	assert.Equal(t, "p1", created.Name)

	got, err := store.Get(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.Name)

	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestPodsGetUnmanagedReturnsNotFound(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: testNS}}
	cs := fake.NewClientset(pod)
	store := k8s.NewPods(cs, testNS)

	_, err := store.Get(t.Context(), "stray")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestPodsGetMissingReturnsNotFound(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.Get(t.Context(), "nope")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestPodsDelete(t *testing.T) {
	store, _ := newStore(t, *managedPod("p1"))
	require.NoError(t, store.Delete(t.Context(), "p1", 0))

	_, err := store.Get(t.Context(), "p1")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestPodsDeleteMissing(t *testing.T) {
	store, _ := newStore(t)
	err := store.Delete(t.Context(), "ghost", 0)
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestPodsFindByID(t *testing.T) {
	store, _ := newStore(t, *managedPod("redis-1"), *managedPod("nginx-1"))

	id := podspec.ContainerID(testNS, "redis-1")
	got, err := store.FindByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "redis-1", got.Name)

	got, err = store.FindByID(t.Context(), podspec.ShortID(id))
	require.NoError(t, err)
	assert.Equal(t, "redis-1", got.Name)
}

func TestPodsFindByIDNoMatch(t *testing.T) {
	store, _ := newStore(t, *managedPod("redis-1"))
	_, err := store.FindByID(t.Context(), "0000000000000000000000000000000000000000000000000000000000000000")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestPodsFindByIDEmpty(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.FindByID(t.Context(), "")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestWaitForReadyReturnsOnReady(t *testing.T) {
	pod := managedPod("p1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodRunning,
		Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		},
	}
	store, _ := newStore(t, *pod)

	require.NoError(t, store.WaitForReady(t.Context(), "p1"))
}

func TestWaitForReadyFailFastOnImagePullBackOff(t *testing.T) {
	pod := managedPod("p1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "Back-off pulling image \"bogus:tag\"",
					},
				},
			},
		},
	}
	store, _ := newStore(t, *pod)

	err := store.WaitForReady(t.Context(), "p1")
	require.Error(t, err)
	var ipf *k8s.ImagePullFailedError
	require.True(t, errors.As(err, &ipf), "expected ImagePullFailedError, got %T", err)
	assert.Equal(t, "ImagePullBackOff", ipf.Reason)
	assert.Contains(t, ipf.Error(), "bogus:tag")
}

func TestWaitForReadyTimesOut(t *testing.T) {
	pod := managedPod("p1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
	store, _ := newStore(t, *pod)

	err := store.WaitForReady(t.Context(), "p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestWaitForReadyHonorsContext(t *testing.T) {
	pod := managedPod("p1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
	store, _ := newStore(t, *pod)
	store.SetReadyTimeout(time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := store.WaitForReady(ctx, "p1")
	require.Error(t, err)
}

func TestWaitForReadyMissingPod(t *testing.T) {
	store, _ := newStore(t)
	err := store.WaitForReady(t.Context(), "ghost")
	require.ErrorIs(t, err, k8s.ErrNotFound)
}

func TestWaitForReadyOnFailedPhase(t *testing.T) {
	pod := managedPod("p1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}
	store, _ := newStore(t, *pod)

	err := store.WaitForReady(t.Context(), "p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed phase")
}
