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
	"github.com/bpaquet/docker-in-kubernetes/internal/images"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// Docker Engine API version we advertise.
const (
	APIVersion    = "1.43"
	MinAPIVersion = "1.24"
)

// PodStore is the subset of *k8s.Pods the handlers use.
type PodStore interface {
	Create(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error)
	Delete(ctx context.Context, name string, grace time.Duration) error
	Get(ctx context.Context, name string) (*corev1.Pod, error)
	List(ctx context.Context) ([]corev1.Pod, error)
	FindByID(ctx context.Context, id string) (*corev1.Pod, error)
	WaitForReady(ctx context.Context, name string) error
	StreamLogs(ctx context.Context, name string, opts k8s.LogOptions) (io.ReadCloser, error)
	Attach(ctx context.Context, podName string, opts k8s.StreamOptions) error
	Exec(ctx context.Context, podName string, cmd []string, opts k8s.StreamOptions) error
	Namespace() string
}

// PortForwarder is the subset of forwarder.Forwarder the handlers use.
type PortForwarder interface {
	Open(ctx context.Context, namespace, pod string, mappings []forwarder.Mapping) (forwarder.Handle, error)
}

// Config configures New.
type Config struct {
	DaemonVersion string
	Logger        *slog.Logger
	Pods          PodStore
	Forwarder     PortForwarder
	Forwards      *forwarder.Registry
	Images        *images.Store
}

// New returns the HTTP handler.
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
			execs:     newExecStore(),
			logger:    logger,
		}
		ch.register(mux)
		ch.registerExec(mux)
	}

	if cfg.Images != nil {
		ih := &imageHandlers{store: cfg.Images, now: time.Now}
		ih.register(mux)
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
		// Reject e.g. "/v1.43abc" — the version must be followed by '/' or EOL.
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

// WriteHeader records the status for logRequests.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.NewResponseController walk past us to the real writer,
// which is what makes hijacking work through this middleware.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

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
