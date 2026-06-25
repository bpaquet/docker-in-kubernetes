package server

import (
	"encoding/json"
	"net/http"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, dockerapi.ErrorResponse{Message: message})
}
