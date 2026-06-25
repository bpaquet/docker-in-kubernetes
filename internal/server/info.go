package server

import (
	"net/http"
	"runtime"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func handleInfo(daemonVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, dockerapi.InfoResponse{
			ID:              "DIK1:DOCKER:IN:KUBERNETES",
			Name:            "docker-in-kubernetes",
			ServerVersion:   daemonVersion,
			OperatingSystem: "kubernetes",
			OSType:          "linux",
			Architecture:    runtime.GOARCH,
			Driver:          "kubernetes",
		})
	}
}
