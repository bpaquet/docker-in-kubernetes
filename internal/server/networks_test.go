package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/networks"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
)

func newNetworkTestServer(t *testing.T) (*httptest.Server, *networks.Store) {
	t.Helper()
	store := networks.New()
	h := server.New(server.Config{DaemonVersion: "0.0.0-test", Networks: store})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, store
}

func TestNetworkInspectMissing(t *testing.T) {
	ts, _ := newNetworkTestServer(t)
	resp, err := http.Get(ts.URL + "/v1.43/networks/bpa_default")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestNetworkCreateThenInspect(t *testing.T) {
	ts, _ := newNetworkTestServer(t)

	body, _ := json.Marshal(dockerapi.NetworkCreateRequest{
		Name:   "bpa_default",
		Driver: "bridge",
		Labels: map[string]string{"com.docker.compose.project": "bpa"},
	})
	resp, err := http.Post(ts.URL+"/v1.43/networks/create", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created dockerapi.NetworkCreateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	assert.NotEmpty(t, created.ID)

	resp, err = http.Get(ts.URL + "/v1.43/networks/bpa_default")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var net dockerapi.Network
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&net))
	assert.Equal(t, "bpa_default", net.Name)
	assert.Equal(t, created.ID, net.ID)
	assert.Equal(t, "bridge", net.Driver)
	assert.Equal(t, "local", net.Scope)
	assert.Equal(t, "bpa", net.Labels["com.docker.compose.project"])
}

func TestNetworkList(t *testing.T) {
	ts, store := newNetworkTestServer(t)
	store.Record("a", "bridge", nil, mustTime(t))
	store.Record("b", "bridge", nil, mustTime(t))

	resp, err := http.Get(ts.URL + "/v1.43/networks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out []dockerapi.Network
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out, 3, "bridge seed + a + b")
}

func TestNetworkCreateConflict(t *testing.T) {
	ts, _ := newNetworkTestServer(t)
	body, _ := json.Marshal(dockerapi.NetworkCreateRequest{Name: "x", Driver: "bridge"})

	resp, err := http.Post(ts.URL+"/v1.43/networks/create", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, err = http.Post(ts.URL+"/v1.43/networks/create", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestNetworkCreateMissingName(t *testing.T) {
	ts, _ := newNetworkTestServer(t)
	resp, err := http.Post(ts.URL+"/v1.43/networks/create", "application/json", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestNetworkInspectBridgeSeed(t *testing.T) {
	ts, _ := newNetworkTestServer(t)
	resp, err := http.Get(ts.URL + "/v1.43/networks/bridge")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNetworkDelete(t *testing.T) {
	ts, store := newNetworkTestServer(t)
	store.Record("bpa_default", "bridge", nil, mustTime(t))

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/networks/bpa_default", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	_, ok := store.Find("bpa_default")
	assert.False(t, ok)
}

func TestNetworkDeleteMissing(t *testing.T) {
	ts, _ := newNetworkTestServer(t)
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/networks/nope", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestNetworkConnectDisconnectNoop(t *testing.T) {
	ts, store := newNetworkTestServer(t)
	store.Record("bpa_default", "bridge", nil, mustTime(t))

	for _, action := range []string{"connect", "disconnect"} {
		resp, err := http.Post(ts.URL+"/v1.43/networks/bpa_default/"+action, "application/json", bytes.NewReader([]byte(`{}`)))
		require.NoError(t, err, action)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, action)
	}
}

func mustTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
}
