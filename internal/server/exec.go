package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	utilexec "k8s.io/client-go/util/exec"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

// execInstance is the daemon-side bookkeeping for one /containers/{id}/exec call.
type execInstance struct {
	ID          string
	ContainerID string
	PodName     string
	Cmd         []string
	Tty         bool
	AttachStdin bool

	mu       sync.Mutex
	running  bool
	exitCode int
}

// execStore is an in-memory map of exec instances keyed by exec ID.
type execStore struct {
	mu sync.Mutex
	m  map[string]*execInstance
}

func newExecStore() *execStore {
	return &execStore{m: make(map[string]*execInstance)}
}

func (s *execStore) put(e *execInstance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[e.ID] = e
}

func (s *execStore) get(id string) *execInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func newExecID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (c *containerHandlers) registerExec(mux *http.ServeMux) {
	mux.HandleFunc("POST /containers/{id}/exec", c.execCreate)
	mux.HandleFunc("POST /exec/{id}/start", c.execStart)
	mux.HandleFunc("GET /exec/{id}/json", c.execInspect)
}

func (c *containerHandlers) execCreate(w http.ResponseWriter, r *http.Request) {
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	var req dockerapi.ExecCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Cmd) == 0 {
		writeError(w, http.StatusBadRequest, "Cmd is required")
		return
	}
	e := &execInstance{
		ID:          newExecID(),
		ContainerID: r.PathValue("id"),
		PodName:     pod.Name,
		Cmd:         req.Cmd,
		Tty:         req.Tty,
		AttachStdin: req.AttachStdin,
	}
	c.execs.put(e)
	writeJSON(w, http.StatusCreated, dockerapi.ExecCreateResponse{ID: e.ID})
}

func (c *containerHandlers) execStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e := c.execs.get(id)
	if e == nil {
		writeError(w, http.StatusNotFound, "no such exec: "+id)
		return
	}

	// Drain the JSON body using ContentLength only — using json.Decoder here
	// risks the decoder buffering past the body into the post-upgrade stdin
	// stream, dropping the first bytes the CLI writes.
	if r.ContentLength > 0 {
		_, _ = io.CopyN(io.Discard, r.Body, r.ContentLength)
	}

	conn, brw, err := hijack(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	if err := writeRawStreamResponse(r, brw); err != nil {
		c.logger.Warn("exec: hijack write failed", "err", err)
		return
	}

	stdout, stderr := multiplexedStdoutStderr(conn, e.Tty)
	opts := k8s.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
		TTY:    e.Tty,
	}
	if e.Tty {
		opts.Stderr = nil
	}
	if e.AttachStdin {
		opts.Stdin = brw
	}

	e.mu.Lock()
	e.running = true
	e.mu.Unlock()

	// Decouple from r.Context() — after hijack the request is "done" and r's
	// context can be cancelled while the stream is still in flight.
	execErr := c.pods.Exec(context.Background(), e.PodName, e.Cmd, opts)

	e.mu.Lock()
	e.running = false
	e.exitCode = exitCodeFromExecErr(execErr)
	e.mu.Unlock()

	if execErr != nil && !errors.Is(execErr, context.Canceled) {
		c.logger.Info("exec ended", "id", e.ID, "container", e.PodName, "err", execErr)
	}
}

// exitCodeFromExecErr extracts the exit status from a remotecommand error.
// A non-zero exit becomes a *utilexec.CodeExitError; anything else collapses
// to 1 (generic failure).
func exitCodeFromExecErr(err error) int {
	if err == nil {
		return 0
	}
	var ce utilexec.CodeExitError
	if errors.As(err, &ce) {
		return ce.ExitStatus()
	}
	return 1
}

func (c *containerHandlers) execInspect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e := c.execs.get(id)
	if e == nil {
		writeError(w, http.StatusNotFound, "no such exec: "+id)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	resp := dockerapi.ExecInspect{
		ID:          e.ID,
		Running:     e.running,
		ExitCode:    e.exitCode,
		OpenStdin:   e.AttachStdin,
		OpenStdout:  true,
		OpenStderr:  !e.Tty,
		ContainerID: e.ContainerID,
		ProcessConfig: dockerapi.ProcessConfig{
			Tty:        e.Tty,
			EntryPoint: firstOrEmpty(e.Cmd),
			Arguments:  restOrNil(e.Cmd),
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func restOrNil(s []string) []string {
	if len(s) <= 1 {
		return nil
	}
	return s[1:]
}
