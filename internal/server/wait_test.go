package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

// TestWaitFlagsExternalPodDeletion locks in that /wait surfaces a pod that
// disappeared mid-wait via WaitResponse.Error rather than masking it as a
// clean exit 0. Real dockerd does the same.
//
// The test deletes the pod AFTER the server has flushed 200 (i.e. after
// http.Post returns headers) — at that point we know the server is inside
// the waitForExit poll loop, so the delete reliably triggers gone=true.
func TestWaitFlagsExternalPodDeletion(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	ts, cs, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")

	resp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, cs.CoreV1().Pods(testNamespace).Delete(context.Background(), "redis-1", metav1.DeleteOptions{}))

	var got dockerapi.WaitResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, int64(0), got.StatusCode)
	require.NotNil(t, got.Error, "external delete should set WaitResponse.Error")
	assert.Contains(t, got.Error.Message, "removed")
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
	ts, _, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	var got dockerapi.WaitResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, int64(42), got.StatusCode)
}

// TestWaitConditionRemovedDoesNotFlagError ensures /wait?condition=removed
// (the docker run --rm path) doesn't trip the external-delete signal —
// we delete the pod AFTER waitForExit returns the real exit code.
func TestWaitConditionRemovedDoesNotFlagError(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodSucceeded,
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}},
	}
	ts, _, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/wait?condition=removed", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	var got dockerapi.WaitResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, int64(0), got.StatusCode)
	assert.Nil(t, got.Error, "condition=removed must not surface as external delete")
}

func TestWaitReturns404ForUnknown(t *testing.T) {
	ts, _, _, _ := newTestHandler(t)
	resp, err := http.Post(ts.URL+"/v1.43/containers/deadbeef/wait", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
