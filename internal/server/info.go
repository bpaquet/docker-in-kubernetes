package server

import (
	"log/slog"
	"net/http"
	"runtime"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func handleInfo(daemonVersion string, pods PodStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total, running := containerCounts(r, pods, logger)
		writeJSON(w, http.StatusOK, dockerapi.InfoResponse{
			ID:                "DIK1:DOCKER:IN:KUBERNETES",
			Name:              "docker-in-kubernetes",
			ServerVersion:     daemonVersion,
			OperatingSystem:   "kubernetes",
			OSType:            "linux",
			Architecture:      runtime.GOARCH,
			NCPU:              runtime.NumCPU(),
			Driver:            "kubernetes",
			Containers:        total,
			ContainersRunning: running,
		})
	}
}

func containerCounts(r *http.Request, pods PodStore, logger *slog.Logger) (total, running int) {
	if pods == nil {
		return 0, 0
	}
	if logger == nil {
		logger = slog.Default()
	}
	list, err := pods.List(r.Context())
	if err != nil {
		logger.Warn("info: list pods failed", "err", err)
		return 0, 0
	}
	for i := range list {
		total++
		if list[i].Status.Phase == corev1.PodRunning {
			running++
		}
	}
	return total, running
}
