package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func TestCreateRejectsInteractive(t *testing.T) {
	for _, field := range []string{"AttachStdin", "AttachStdout", "AttachStderr", "OpenStdin", "Tty"} {
		t.Run(field, func(t *testing.T) {
			ts, _, _ := newTestHandler(t)

			req := dockerapi.CreateRequest{Image: "redis"}
			switch field {
			case "AttachStdin":
				req.AttachStdin = true
			case "AttachStdout":
				req.AttachStdout = true
			case "AttachStderr":
				req.AttachStderr = true
			case "OpenStdin":
				req.OpenStdin = true
			case "Tty":
				req.Tty = true
			}
			body, _ := json.Marshal(req)
			resp, err := http.Post(ts.URL+"/v1.43/containers/create", "application/json", bytes.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

			var errBody dockerapi.ErrorResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
			assert.Contains(t, errBody.Message, "use -d")
		})
	}
}
