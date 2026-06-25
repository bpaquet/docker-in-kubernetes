// Package server implements the Docker Engine HTTP API subset.
package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// APIVersion is the Docker Engine API version we advertise.
// MinAPIVersion is the floor reported in /version.
const (
	APIVersion    = "1.43"
	MinAPIVersion = "1.24"
)

// PodStore is the subset of internal/k8s.Pods the server handlers need.
type PodStore interface {
	Create(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error)
	Delete(ctx context.Context, name string, grace time.Duration) error
	Get(ctx context.Context, name string) (*corev1.Pod, error)
	List(ctx context.Context) ([]corev1.Pod, error)
	FindByID(ctx context.Context, id string) (*corev1.Pod, error)
	WaitForReady(ctx context.Context, name string) error
	StreamLogs(ctx context.Context, name string, opts k8s.LogOptions) (io.ReadCloser, error)
	Namespace() string
}

// PortForwarder is the subset of internal/forwarder.Forwarder used here.
type PortForwarder interface {
	Open(ctx context.Context, namespace, pod string, mappings []forwarder.Mapping) (forwarder.Handle, error)
}

// Config configures the HTTP handler returned by New.
type Config struct {
	DaemonVersion string
	Logger        *slog.Logger
	Pods          PodStore
	Forwarder     PortForwarder
	Forwards      *forwarder.Registry
}

// New returns the HTTP handler implementing the Docker Engine API subset.
func New(cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", handlePing)
	mux.HandleFunc("HEAD /_ping", handlePing)
	mux.HandleFunc("GET /version", handleVersion(cfg.DaemonVersion))
	mux.HandleFunc("GET /info", handleInfo(cfg.DaemonVersion))

	if cfg.Pods != nil && cfg.Forwarder != nil && cfg.Forwards != nil {
		ch := &containerHandlers{
			pods:      cfg.Pods,
			forwarder: cfg.Forwarder,
			registry:  cfg.Forwards,
			logger:    logger,
		}
		ch.register(mux)
	}

	return chain(mux, stripVersionPrefix, logRequests(logger))
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

func stripVersionPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		loc := versionPrefix.FindStringIndex(path)
		if loc == nil {
			next.ServeHTTP(w, r)
			return
		}
		end := loc[1]
		// Boundary must be '/' or end-of-string; otherwise "/v1.43abc" would
		// be wrongly recognized as a version prefix.
		if end < len(path) && path[end] != '/' {
			next.ServeHTTP(w, r)
			return
		}
		stripped := path[end:]
		if stripped == "" {
			stripped = "/"
		}
		r2 := r.Clone(r.Context())
		newURL := *r.URL
		newURL.Path = stripped
		r2.URL = &newURL
		next.ServeHTTP(w, r2)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status before delegating, for the request logger.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
			)
		})
	}
}

func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func setDockerHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Api-Version", APIVersion)
	h.Set("Ostype", "linux")
	h.Set("Server", "docker-in-kubernetes")
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	setDockerHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", "2")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte("OK"))
}

type versionResponse struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	GitCommit     string `json:"GitCommit"`
	GoVersion     string `json:"GoVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
	BuildTime     string `json:"BuildTime"`
}

func handleVersion(daemonVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		setDockerHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		resp := versionResponse{
			Version:       daemonVersion,
			APIVersion:    APIVersion,
			MinAPIVersion: MinAPIVersion,
			GoVersion:     runtime.Version(),
			Os:            "linux",
			Arch:          runtime.GOARCH,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
