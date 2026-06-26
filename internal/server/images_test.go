package server_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/images"
	"github.com/bpaquet/docker-in-kubernetes/internal/server"
)

func mustPull(t *testing.T, ts *httptest.Server, image, tag string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1.43/images/create?fromImage="+image+"&tag="+tag, "", nil)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func newImageTestServer(t *testing.T) (*httptest.Server, *images.Store) {
	t.Helper()
	store := images.New()
	h := server.New(server.Config{DaemonVersion: "0.0.0-test", Images: store})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, store
}

func TestImagesCreate(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantRef string
	}{
		{"ref+tag", "fromImage=redis&tag=alpine", "redis:alpine"},
		{"no tag defaults to latest", "fromImage=redis", "redis:latest"},
		{"digest ignores tag", "fromImage=redis@sha256:deadbeef&tag=ignored", "redis@sha256:deadbeef"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, store := newImageTestServer(t)
			resp, err := http.Post(ts.URL+"/v1.43/images/create?"+tc.query, "", nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

			var msgs []map[string]string
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				var m map[string]string
				require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
				msgs = append(msgs, m)
			}
			require.GreaterOrEqual(t, len(msgs), 2)
			assert.True(t, strings.HasPrefix(msgs[len(msgs)-1]["status"], "Status: Image is up to date for "))

			assert.True(t, store.Has(tc.wantRef), "store should contain %q, got %+v", tc.wantRef, store.List())
		})
	}
}

func TestImagesCreateMissingFromImage(t *testing.T) {
	ts, store := newImageTestServer(t)
	resp, err := http.Post(ts.URL+"/v1.43/images/create", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, store.List())
}

func TestImagesList(t *testing.T) {
	ts, store := newImageTestServer(t)
	mustPull(t, ts, "redis", "alpine")
	mustPull(t, ts, "postgres", "16")

	resp, err := http.Get(ts.URL + "/v1.43/images/json")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out []dockerapi.ImageSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 2)

	refs := []string{out[0].RepoTags[0], out[1].RepoTags[0]}
	assert.ElementsMatch(t, []string{"redis:alpine", "postgres:16"}, refs)
	for _, s := range out {
		assert.True(t, strings.HasPrefix(s.ID, "sha256:"))
		assert.Equal(t, -1, s.Containers)
	}
	// list reflects the live store
	assert.Len(t, store.List(), 2)
}

func TestImagesInspect(t *testing.T) {
	ts, _ := newImageTestServer(t)
	mustPull(t, ts, "redis", "alpine")

	t.Run("by ref", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/v1.43/images/redis:alpine/json")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var got dockerapi.ImageInspect
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, []string{"redis:alpine"}, got.RepoTags)
		assert.True(t, strings.HasPrefix(got.ID, "sha256:"))
		assert.Equal(t, "linux", got.Os)
	})

	t.Run("not found", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/v1.43/images/unknown:tag/json")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestImagesDelete(t *testing.T) {
	ts, store := newImageTestServer(t)
	mustPull(t, ts, "redis", "alpine")

	t.Run("by ref", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/images/redis:alpine", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var out []dockerapi.ImageDeleteItem
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		require.Len(t, out, 2)
		assert.Equal(t, "redis:alpine", out[0].Untagged)
		assert.True(t, strings.HasPrefix(out[1].Deleted, "sha256:"))
		assert.False(t, store.Has("redis:alpine"))
	})

	t.Run("not found", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1.43/images/nope:tag", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "No such image")
	})
}
