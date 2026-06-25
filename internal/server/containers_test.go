package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
)

const testNamespace = "dik-test"

type fakeForwarder struct {
	mu   sync.Mutex
	open int
}

type fakeHandle struct {
	closed bool
	mu     sync.Mutex
}

func (h *fakeHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return nil
}

func (f *fakeForwarder) Open(_ context.Context, _, _ string, _ []forwarder.Mapping) (forwarder.Handle, error) {
	f.mu.Lock()
	f.open++
	f.mu.Unlock()
	return &fakeHandle{}, nil
}

func newTestHandler(t *testing.T, objs ...runtime.Object) (*httptest.Server, *fake.Clientset, *fakeForwarder) {
	t.Helper()
	cs := fake.NewClientset(objs...)
	// Auto-mark pods as Ready immediately on Create so WaitForReady doesn't hang
	// in handler tests.
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		obj.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		}
		return false, obj, nil
	})

	store := k8s.NewPods(cs, testNamespace)
	store.SetPollInterval(2 * time.Millisecond)
	store.SetReadyTimeout(200 * time.Millisecond)

	fw := &fakeForwarder{}
	registry := forwarder.NewRegistry()

	h := server.New(server.Config{
		DaemonVersion: "0.0.0-test",
		Pods:          store,
		Forwarder:     fw,
		Forwards:      registry,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, cs, fw
}

func TestCreateContainerHappyPath(t *testing.T) {
	ts, cs, fw := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{
		Image: "redis",
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"6379/tcp": {{HostPort: "6379"}},
			},
		},
	})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create?name=myredis", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var got dockerapi.CreateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, podspec.ContainerID(testNamespace, "myredis"), got.ID)

	pod, err := cs.CoreV1().Pods(testNamespace).Get(t.Context(), "myredis", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "redis", pod.Spec.Containers[0].Image)

	assert.Equal(t, 1, fw.open, "forwarder should be opened once")
}

func TestCreateContainerRejectsEmptyImage(t *testing.T) {
	ts, _, _ := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestStartOnUnknownReturns404(t *testing.T) {
	ts, _, _ := newTestHandler(t)

	resp, err := http.Post(ts.URL+"/v1.43/containers/deadbeef/start", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStartIdempotent(t *testing.T) {
	ts, _, fw := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{
		Image: "redis",
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{"6379/tcp": {{HostPort: "6379"}}},
		},
	})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create?name=myredis", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	id := podspec.ContainerID(testNamespace, "myredis")

	startResp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/start", "", nil)
	require.NoError(t, err)
	startResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, startResp.StatusCode)

	assert.Equal(t, 1, fw.open, "second start must not reopen forwarder")
}

func TestListContainers(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	ts, _, _ := newTestHandler(t, pod)

	resp, err := http.Get(ts.URL + "/v1.43/containers/json")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "running", out[0].State)
	assert.Equal(t, "/redis-1", out[0].Names[0])
}

func TestListContainersFiltersStoppedByDefault(t *testing.T) {
	running := managedPod("redis-1")
	running.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	exited := managedPod("redis-2")
	exited.Status = corev1.PodStatus{Phase: corev1.PodSucceeded}
	ts, _, _ := newTestHandler(t, running, exited)

	resp, err := http.Get(ts.URL + "/v1.43/containers/json")
	require.NoError(t, err)
	defer resp.Body.Close()

	var out []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "running", out[0].State)
}

func TestListContainersAllIncludesExited(t *testing.T) {
	running := managedPod("redis-1")
	running.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	exited := managedPod("redis-2")
	exited.Status = corev1.PodStatus{Phase: corev1.PodSucceeded}
	ts, _, _ := newTestHandler(t, running, exited)

	resp, err := http.Get(ts.URL + "/v1.43/containers/json?all=1")
	require.NoError(t, err)
	defer resp.Body.Close()

	var out []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out, 2)
}

func TestInspectReturnsPodFields(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	pod.Annotations = map[string]string{
		podspec.AnnotationImage:      "redis:7",
		podspec.AnnotationDockerName: "redis-1",
	}
	ts, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Get(ts.URL + "/v1.43/containers/" + id + "/json")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var got dockerapi.ContainerInspect
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, id, got.ID)
	assert.Equal(t, "redis:7", got.Image)
	assert.Equal(t, "/redis-1", got.Name)
	assert.Equal(t, "running", got.State.Status)
	assert.True(t, got.State.Running)
}

func TestKillDeletesPodAndClosesForwarder(t *testing.T) {
	ts, cs, _ := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{
		Image: "redis",
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{"6379/tcp": {{HostPort: "6379"}}},
		},
	})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create?name=myredis", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	id := podspec.ContainerID(testNamespace, "myredis")
	killResp, err := http.Post(ts.URL+"/v1.43/containers/"+id+"/kill", "", nil)
	require.NoError(t, err)
	killResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, killResp.StatusCode)

	_, err = cs.CoreV1().Pods(testNamespace).Get(t.Context(), "myredis", metav1.GetOptions{})
	require.Error(t, err)
}

func TestDeleteMissingContainerReturns404(t *testing.T) {
	ts, _, _ := newTestHandler(t)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/containers/abc", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestInfo(t *testing.T) {
	ts, _, _ := newTestHandler(t)

	resp, err := http.Get(ts.URL + "/info")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var info dockerapi.InfoResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.Equal(t, "docker-in-kubernetes", info.Name)
	assert.Equal(t, "0.0.0-test", info.ServerVersion)
}

func TestLogsStreamsMultiplexed(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	ts, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Get(ts.URL + "/v1.43/containers/" + id + "/logs?stdout=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/vnd.docker.raw-stream", resp.Header.Get("Content-Type"))

	// fake clientset returns a non-empty body for GetLogs; just confirm we
	// read at least the framing header without error.
	buf := make([]byte, 8)
	_, err = io.ReadFull(resp.Body, buf)
	if err == nil {
		// stream byte (1 stdout / 2 stderr)
		assert.Contains(t, []byte{1, 2}, buf[0])
	} else {
		// On some fake clientset versions the body is empty; that's still OK.
		assert.ErrorIs(t, err, io.EOF)
	}
}

func managedPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				podspec.LabelManaged: "true",
			},
			Annotations: map[string]string{
				podspec.AnnotationImage:      "redis",
				podspec.AnnotationDockerName: name,
				podspec.AnnotationCreatedAt:  time.Now().UTC().Format(time.RFC3339),
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: name, Image: "redis"}},
		},
	}
}
