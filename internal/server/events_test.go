package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/server"
)

// In-process test: bypass HTTP transport entirely, exercise ServeHTTP directly.
// This sidesteps the keep-alive / client-disconnect plumbing that makes
// end-to-end testing of a hold-open stream brittle.
func TestEventsHoldsOpenInProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, "/v1.43/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h := server.New(server.Config{DaemonVersion: "test"})

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("handler returned before ctx cancel")
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit on ctx cancel within 2s")
	}

	res := rec.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	body, _ := io.ReadAll(res.Body)
	assert.Empty(t, body, "events stream should emit nothing")
}

// End-to-end sanity check: status + headers via real HTTP, then bail.
func TestEventsResponseHeaders(t *testing.T) {
	ts := newTestServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1.43/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}
