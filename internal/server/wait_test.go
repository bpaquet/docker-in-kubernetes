package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

func TestWaitReturns0OnPodDeletion(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	ts, cs, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = cs.CoreV1().Pods(testNamespace).Delete(context.Background(), "redis-1", metav1.DeleteOptions{})
	}()

	resp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got dockerapi.WaitResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, int64(0), got.StatusCode)
}

func TestWaitReturnsContainerExitCodeOnFailure(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{
			{
				Name:  "main",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}},
			},
		},
	}
	ts, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	var got dockerapi.WaitResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, int64(42), got.StatusCode)
}

func TestWaitReturns404ForUnknown(t *testing.T) {
	ts, _, _ := newTestHandler(t)
	resp, err := http.Post(ts.URL+"/v1.43/containers/deadbeef/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
