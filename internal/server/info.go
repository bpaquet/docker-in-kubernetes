package server

import (
	"net/http"
	"runtime"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func handleInfo(daemonVersion string, pods PodStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total, running := containerCounts(r, pods)
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

func containerCounts(r *http.Request, pods PodStore) (total, running int) {
	if pods == nil {
		return 0, 0
	}
	list, err := pods.List(r.Context())
	if err != nil {
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
