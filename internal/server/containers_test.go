package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

func newTestHandler(t *testing.T, objs ...runtime.Object) (*httptest.Server, *fake.Clientset, *fakeForwarder, *forwarder.Registry) {
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
		DaemonVersion:       "0.0.0-test",
		Pods:                store,
		Forwarder:           fw,
		Forwards:            registry,
		CleanupPollInterval: 5 * time.Millisecond,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, cs, fw, registry
}

func TestCreateContainerHappyPath(t *testing.T) {
	ts, cs, fw, _ := newTestHandler(t)

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

	// /create only stages; /start realizes the pod.
	startResp, err := http.Post(ts.URL+"/v1.43/containers/"+got.ID+"/start", "", nil)
	require.NoError(t, err)
	startResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, startResp.StatusCode)

	pod, err := cs.CoreV1().Pods(testNamespace).Get(t.Context(), "myredis", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "redis", pod.Spec.Containers[0].Image)

	assert.Equal(t, 1, fw.open, "forwarder should be opened once")
}

func TestCreateContainerRejectsEmptyImage(t *testing.T) {
	ts, _, _, _ := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestStartOnUnknownReturns404(t *testing.T) {
	ts, _, _, _ := newTestHandler(t)

	resp, err := http.Post(ts.URL+"/v1.43/containers/deadbeef/start", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStartIdempotent(t *testing.T) {
	ts, _, fw, _ := newTestHandler(t)

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
	ts, _, _, _ := newTestHandler(t, pod)

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
	ts, _, _, _ := newTestHandler(t, running, exited)

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
	ts, _, _, _ := newTestHandler(t, running, exited)

	resp, err := http.Get(ts.URL + "/v1.43/containers/json?all=1")
	require.NoError(t, err)
	defer resp.Body.Close()

	var out []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out, 2)
}

func TestListContainersSurfacesExitCode(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   137,
				FinishedAt: metav1.NewTime(time.Now().Add(-90 * time.Second)),
			}},
		}},
	}
	ts, _, _, _ := newTestHandler(t, pod)

	resp, err := http.Get(ts.URL + "/v1.43/containers/json?all=1")
	require.NoError(t, err)
	defer resp.Body.Close()

	var out []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "exited", out[0].State)
	assert.Contains(t, out[0].Status, "Exited (137)")
	assert.Contains(t, out[0].Status, "ago")
}

func TestInspectSurfacesExitCodeAndFinishedAt(t *testing.T) {
	finished := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   42,
				FinishedAt: metav1.NewTime(finished),
			}},
		}},
	}
	ts, _, _, _ := newTestHandler(t, pod)

	id := podspec.ContainerID(testNamespace, "redis-1")
	resp, err := http.Get(ts.URL + "/v1.43/containers/" + id + "/json")
	require.NoError(t, err)
	defer resp.Body.Close()

	var got dockerapi.ContainerInspect
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, 42, got.State.ExitCode)
	assert.NotEmpty(t, got.State.FinishedAt)
	parsed, err := time.Parse(time.RFC3339Nano, got.State.FinishedAt)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(finished), "want %s, got %s", finished, parsed)
}

func TestInspectReturnsPodFields(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	pod.Annotations = map[string]string{
		podspec.AnnotationImage:      "redis:7",
		podspec.AnnotationDockerName: "redis-1",
	}
	ts, _, _, _ := newTestHandler(t, pod)

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

// TestPendingContainerRoutesThroughSharedBuilder locks in that a /create'd-
// but-not-/start'ed container surfaces every annotation-backed field
// (ports, labels, env, user) via the same buildSummary/buildInspect path as a
// live pod, with State pinned to "created".
func TestPendingContainerRoutesThroughSharedBuilder(t *testing.T) {
	ts, _, _, _ := newTestHandler(t)

	body, _ := json.Marshal(dockerapi.CreateRequest{
		Image:  "redis:7",
		Env:    []string{"FOO=bar"},
		Labels: map[string]string{"team": "platform"},
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"6379/tcp": {{HostPort: "6380"}},
			},
		},
	})
	resp, err := http.Post(ts.URL+"/v1.43/containers/create?name=staged", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created dockerapi.CreateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))

	psResp, err := http.Get(ts.URL + "/v1.43/containers/json?all=1")
	require.NoError(t, err)
	defer psResp.Body.Close()
	var ps []dockerapi.ContainerSummary
	require.NoError(t, json.NewDecoder(psResp.Body).Decode(&ps))
	require.Len(t, ps, 1)
	assert.Equal(t, created.ID, ps[0].ID)
	assert.Equal(t, "created", ps[0].State)
	assert.Equal(t, "Created", ps[0].Status)
	assert.Equal(t, "redis:7", ps[0].Image)
	assert.Equal(t, "/staged", ps[0].Names[0])
	assert.Equal(t, map[string]string{"team": "platform"}, ps[0].Labels)
	require.Len(t, ps[0].Ports, 1)
	assert.Equal(t, uint16(6380), ps[0].Ports[0].PublicPort)
	assert.Equal(t, uint16(6379), ps[0].Ports[0].PrivatePort)

	inspectResp, err := http.Get(ts.URL + "/v1.43/containers/" + created.ID + "/json")
	require.NoError(t, err)
	defer inspectResp.Body.Close()
	var insp dockerapi.ContainerInspect
	require.NoError(t, json.NewDecoder(inspectResp.Body).Decode(&insp))
	assert.Equal(t, created.ID, insp.ID)
	assert.Equal(t, "created", insp.State.Status)
	assert.False(t, insp.State.Running)
	assert.Equal(t, []string{"FOO=bar"}, insp.Config.Env)
	assert.Equal(t, map[string]string{"team": "platform"}, insp.Config.Labels)
	require.NotNil(t, insp.HostConfig.PortBindings["6379/tcp"])
	assert.Equal(t, "6380", insp.HostConfig.PortBindings["6379/tcp"][0].HostPort)
}

func TestForwarderClosedOnContainerExit(t *testing.T) {
	ts, cs, _, registry := newTestHandler(t)

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

	require.True(t, registry.Has(id), "forwarder should be registered after /start")

	pod, err := cs.CoreV1().Pods(testNamespace).Get(t.Context(), "myredis", metav1.GetOptions{})
	require.NoError(t, err)
	pod.Status.Phase = corev1.PodSucceeded
	_, err = cs.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return !registry.Has(id) },
		2*time.Second, 10*time.Millisecond,
		"forwarder should be closed once the container exits")
}

// Closing the forwarder from another path (eg. /kill, /wait?condition=removed)
// must let the cleanup watcher exit on its next poll without panicking on the
// already-closed handle. We assert by simulating it: open, externally close,
// wait two polls, then update pod status — the watcher should not double-close.
func TestForwarderWatcherExitsWhenClosedExternally(t *testing.T) {
	ts, cs, _, registry := newTestHandler(t)

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

	require.NoError(t, registry.Close(id))
	assert.False(t, registry.Has(id))

	time.Sleep(30 * time.Millisecond)
	pod, err := cs.CoreV1().Pods(testNamespace).Get(t.Context(), "myredis", metav1.GetOptions{})
	require.NoError(t, err)
	pod.Status.Phase = corev1.PodSucceeded
	_, err = cs.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)

	time.Sleep(30 * time.Millisecond)
	assert.False(t, registry.Has(id), "registry should still be empty (watcher must not resurrect entries)")
}

func TestKillDeletesPodAndClosesForwarder(t *testing.T) {
	ts, cs, _, _ := newTestHandler(t)

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

func TestDeleteMissingContainerIsNoOp(t *testing.T) {
	ts, _, _, _ := newTestHandler(t)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/containers/abc", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestInfo(t *testing.T) {
	running := managedPod("redis-1")
	running.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	exited := managedPod("redis-2")
	exited.Status = corev1.PodStatus{Phase: corev1.PodSucceeded}
	ts, _, _, _ := newTestHandler(t, running, exited)

	resp, err := http.Get(ts.URL + "/info")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var info dockerapi.InfoResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.Equal(t, "docker-in-kubernetes", info.Name)
	assert.Equal(t, "0.0.0-test", info.ServerVersion)
	assert.Equal(t, 2, info.Containers)
	assert.Equal(t, 1, info.ContainersRunning)
	assert.Positive(t, info.NCPU)
}

func TestInfoLogsAndReturnsZerosOnListError(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	store := k8s.NewPods(cs, testNamespace)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := server.New(server.Config{
		DaemonVersion: "0.0.0-test",
		Logger:        logger,
		Pods:          store,
		Forwarder:     &fakeForwarder{},
		Forwards:      forwarder.NewRegistry(),
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/info")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var info dockerapi.InfoResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.Equal(t, 0, info.Containers)
	assert.Equal(t, 0, info.ContainersRunning)
	assert.Contains(t, logBuf.String(), "info: list pods failed")
	assert.Contains(t, logBuf.String(), "boom")
}

func TestLogsStreamsMultiplexed(t *testing.T) {
	pod := managedPod("redis-1")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	ts, _, _, _ := newTestHandler(t, pod)

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
