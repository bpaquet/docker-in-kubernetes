package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/server"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := server.New(server.Config{DaemonVersion: "0.0.0-test"})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestPing(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"unversioned", "/_ping"},
		{"v1.43 prefix", "/v1.43/_ping"},
		{"v1.24 prefix", "/v1.24/_ping"},
	}
	ts := newTestServer(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, server.APIVersion, resp.Header.Get("Api-Version"))
			assert.Equal(t, "linux", resp.Header.Get("Ostype"))
			assert.Equal(t, "docker-in-kubernetes", resp.Header.Get("Server"))

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, "OK", string(body))
		})
	}
}

func TestPingHEAD(t *testing.T) {
	ts := newTestServer(t)

	req, err := http.NewRequest(http.MethodHead, ts.URL+"/_ping", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, server.APIVersion, resp.Header.Get("Api-Version"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body, "HEAD must not return a body")
}

func TestVersion(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{"/version", "/v1.43/version"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			assert.Equal(t, server.APIVersion, resp.Header.Get("Api-Version"))

			var got struct {
				Version       string `json:"Version"`
				APIVersion    string `json:"ApiVersion"`
				MinAPIVersion string `json:"MinAPIVersion"`
				GoVersion     string `json:"GoVersion"`
				Os            string `json:"Os"`
				Arch          string `json:"Arch"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

			assert.Equal(t, "0.0.0-test", got.Version)
			assert.Equal(t, server.APIVersion, got.APIVersion)
			assert.Equal(t, server.MinAPIVersion, got.MinAPIVersion)
			assert.Equal(t, runtime.Version(), got.GoVersion)
			assert.Equal(t, "linux", got.Os)
			assert.Equal(t, runtime.GOARCH, got.Arch)
		})
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/containers/json")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestVersionPrefixWithUnknownEndpointStripsAndReturns404(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/v1.43/totally/made/up")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestVersionPrefixEdgeCases(t *testing.T) {
	// Paths that look like a version prefix but aren't a real /vX.Y/ segment
	// must NOT be stripped.
	tests := []struct {
		name           string
		path           string
		expectedStatus int
	}{
		{"no prefix unknown", "/foo", http.StatusNotFound},
		{"version-like word", "/version2/_ping", http.StatusNotFound},
		{"single digit version is not matched", "/v1/_ping", http.StatusNotFound},
		{"trailing chars after version are not a prefix", "/v1.43abc/_ping", http.StatusNotFound},
		{"properly matches /v1.0", "/v1.0/_ping", http.StatusOK},
	}
	ts := newTestServer(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.expectedStatus, resp.StatusCode)
		})
	}
}
