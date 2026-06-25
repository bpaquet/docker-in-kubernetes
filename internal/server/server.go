// Package server implements the Docker Engine HTTP API subset exposed by
// docker-in-kubernetes. It returns a plain http.Handler so callers own the
// listener (UNIX socket, TCP, httptest, ...).
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"runtime"
)

// APIVersion is the Docker Engine API version we advertise.
const APIVersion = "1.43"

// MinAPIVersion is the floor advertised in /version responses.
const MinAPIVersion = "1.24"

// Config configures the HTTP handler.
type Config struct {
	// DaemonVersion is reported as Version in /version.
	DaemonVersion string
	// Logger is used for request logging. nil falls back to slog.Default().
	Logger *slog.Logger
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

	return chain(
		mux,
		stripVersionPrefix,
		logRequests(logger),
	)
}

// versionPrefix matches Docker's optional /vX.Y URL prefix without consuming
// the boundary character. The boundary must be '/' or end-of-string; that is
// verified separately so paths like "/v1.43abc" are not stripped.
var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// stripVersionPrefix rewrites /v1.43/foo -> /foo so a single set of routes
// covers both versioned and unversioned requests. Paths whose version-like
// prefix is followed by something other than '/' or end-of-string are left
// untouched.
func stripVersionPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		loc := versionPrefix.FindStringIndex(path)
		if loc == nil {
			next.ServeHTTP(w, r)
			return
		}
		end := loc[1]
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

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status code before delegating, so the request
// logger can record it.
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

// setDockerHeaders sets the response headers Docker clients use for negotiation.
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

// versionResponse mirrors the Docker Engine /version JSON shape.
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
