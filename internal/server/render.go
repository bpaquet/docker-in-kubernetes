package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Headers + status are already on the wire; we can't switch to 500.
		// At least leave a trail so a non-encodable response is diagnosable.
		slog.Default().Warn("render: json encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, dockerapi.ErrorResponse{Message: message})
}
