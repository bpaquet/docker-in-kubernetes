package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

type containerHandlers struct {
	pods      PodStore
	forwarder PortForwarder
	registry  *forwarder.Registry
	execs     *execStore
	pending   *pendingStore
	logger    *slog.Logger
}

func (c *containerHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /containers/create", c.create)
	mux.HandleFunc("POST /containers/{id}/start", c.start)
	mux.HandleFunc("POST /containers/{id}/stop", c.stop)
	mux.HandleFunc("POST /containers/{id}/kill", c.kill)
	mux.HandleFunc("POST /containers/{id}/wait", c.wait)
	mux.HandleFunc("POST /containers/{id}/attach", c.attach)
	mux.HandleFunc("POST /containers/{id}/resize", c.resize)
	mux.HandleFunc("DELETE /containers/{id}", c.delete)
	mux.HandleFunc("GET /containers/{id}/json", c.inspect)
	mux.HandleFunc("GET /containers/{id}/logs", c.logs)
	mux.HandleFunc("GET /containers/json", c.list)
}

// resize is a no-op stub: TTY resize forwarding needs a TerminalSizeQueue
// wired into the in-flight SPDY attach, which we don't yet plumb. Returning
// 200 keeps the docker CLI from logging a warning on every keystroke.
func (c *containerHandlers) resize(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.resolvePod(w, r); !ok {
		return
	}
	w.WriteHeader(http.StatusOK)
}

// attach hijacks the HTTP connection. For a pending container we hijack +
// write upgrade headers first (so the CLI's subscription is "established"),
// then block until /start realizes the pod, then SPDY-attach to it.
func (c *containerHandlers) attach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wantStdin := boolQuery(r, "stdin")

	pending := c.pending.getByRef(id)
	var pod *corev1.Pod
	if pending == nil {
		var ok bool
		pod, ok = c.resolvePod(w, r)
		if !ok {
			return
		}
	}

	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	if err := writeRawStreamResponse(r, brw); err != nil {
		c.logger.Warn("attach: hijack write failed", "err", err)
		return
	}

	if pending != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := pending.waitForStart(ctx); err != nil {
			c.logger.Info("attach: pending wait failed", "container", pending.Spec.Name, "err", err)
			return
		}
		pod = pending.Spec
	}

	// Wait until kubelet has actually started the container — SPDY attach and
	// the logs API both fail if the pod is still in ContainerCreating.
	startedCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.waitForContainerStarted(startedCtx, pod.Name); err != nil {
		c.logger.Warn("attach: container never started", "container", pod.Name, "err", err)
		return
	}

	tty := len(pod.Spec.Containers) > 0 && pod.Spec.Containers[0].TTY
	if wantStdin {
		c.attachInteractive(pod, conn, tty)
		return
	}
	c.attachLogs(pod, conn, tty)
}

// attachInteractive uses the raw conn for stdin (no /attach body, so bufio's
// pre-buffer is empty anyway).
func (c *containerHandlers) attachInteractive(pod *corev1.Pod, conn net.Conn, tty bool) {
	stdout, stderr := multiplexedStdoutStderr(conn, tty)
	opts := k8s.StreamOptions{Stdin: conn, Stdout: stdout, TTY: tty}
	if !tty {
		opts.Stderr = stderr
	}
	if err := c.pods.Attach(context.Background(), pod.Name, opts); err != nil {
		c.logger.Info("attach ended", "container", pod.Name, "err", err)
	}
}

func (c *containerHandlers) attachLogs(pod *corev1.Pod, conn io.Writer, tty bool) {
	rc, err := c.pods.StreamLogs(context.Background(), pod.Name, k8s.LogOptions{Follow: true})
	if err != nil {
		c.logger.Warn("attach-logs: open failed", "container", pod.Name, "err", err)
		return
	}
	defer func() { _ = rc.Close() }()

	if tty {
		_, _ = io.Copy(conn, rc)
		return
	}
	br := bufio.NewReader(rc)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if writeErr := writeMultiplexedChunk(conn, 1, line); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// create stages the pod spec in memory; the pod isn't realized in k8s until
// /start. Mirrors Docker's "created" state and lets the CLI's create→attach
// →start flow set up attach before the container actually runs.
func (c *containerHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req dockerapi.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "Image is required")
		return
	}

	dockerName := r.URL.Query().Get("name")
	built, err := podspec.Build(podspec.BuildInput{
		Namespace:  c.pods.Namespace(),
		DockerName: dockerName,
		Now:        time.Now().UTC(),
		Request:    req,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id := podspec.ContainerID(c.pods.Namespace(), built.PodName)

	if dockerName != "" {
		// Match docker semantics: duplicate --name → 409.
		if c.pending.getByRef(dockerName) != nil {
			writeError(w, http.StatusConflict, "Conflict. The container name \""+dockerName+"\" is already in use")
			return
		}
		if _, err := c.pods.Get(r.Context(), built.PodName); err == nil {
			writeError(w, http.StatusConflict, "Conflict. The container name \""+dockerName+"\" is already in use")
			return
		}
	}

	c.pending.put(&pendingContainer{
		ID:         id,
		DockerName: dockerName,
		Spec:       built.Pod,
		Mappings:   built.PortMappings,
		CreatedAt:  time.Now().UTC(),
		startCh:    make(chan struct{}),
	})
	writeJSON(w, http.StatusCreated, dockerapi.CreateResponse{ID: id, Warnings: []string{}})
}

// start realizes a /create'd pod (the common path) or no-ops if the pod is
// already in k8s.
func (c *containerHandlers) start(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if p := c.pending.getByRef(id); p != nil {
		if _, err := c.pods.Create(r.Context(), p.Spec); err != nil {
			p.markFailed(err)
			c.pending.remove(p.ID)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Signal waiters (attach/wait subscribers) as soon as the pod is in
		// k8s — they handle their own kubelet-state polling. Marking after
		// ensureRunning would deadlock TTY+stdin pods whose Ready condition
		// kubelet only flips on first attach.
		p.markStarted()
		c.pending.remove(p.ID)
		if err := c.ensureRunning(r.Context(), p.Spec.Name, p.Mappings); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	pod, err := c.pods.FindByID(r.Context(), id)
	if errors.Is(err, k8s.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such container: "+id)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mappings, err := loadMappingsFromPod(pod)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := c.ensureRunning(r.Context(), pod.Name, mappings); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wait blocks until the container exits, then returns the exit code. Headers
// are flushed up front so the CLI's wait subscription is "established" and
// the rest of `docker run` can proceed. condition=removed also deletes the
// pod after exit — basis of `docker run --rm`.
func (c *containerHandlers) wait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pending := c.pending.getByRef(id)

	// Reject unknown refs BEFORE flushing — once we've sent 200 we can't 404.
	if pending == nil {
		if _, err := c.pods.FindByID(r.Context(), id); err != nil {
			if errors.Is(err, k8s.ErrNotFound) {
				writeError(w, http.StatusNotFound, "no such container: "+id)
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()

	if pending != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := pending.waitForStart(ctx); err != nil {
			return
		}
	}

	pod, err := c.pods.FindByID(r.Context(), id)
	if err != nil {
		return
	}
	exit, err := c.waitForExit(r.Context(), pod.Name)
	if err != nil {
		return
	}
	if r.URL.Query().Get("condition") == "removed" {
		_ = c.registry.Close(podspec.ContainerID(pod.Namespace, pod.Name))
		_ = c.pods.Delete(r.Context(), pod.Name, 0)
	}
	_ = json.NewEncoder(w).Encode(dockerapi.WaitResponse{StatusCode: exit})
}

func (c *containerHandlers) waitForExit(ctx context.Context, name string) (int64, error) {
	const poll = 500 * time.Millisecond
	for {
		pod, err := c.pods.Get(ctx, name)
		if errors.Is(err, k8s.ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if exit, done := exitCodeFromPod(pod); done {
			return exit, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// exitCodeFromPod prefers ContainerStatuses[].State.Terminated over Phase —
// kubelet updates the container state before pod.Phase transitions, so a
// fast-exiting container can have ExitCode=7 while Phase is still Running.
func exitCodeFromPod(pod *corev1.Pod) (int64, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			return int64(cs.State.Terminated.ExitCode), true
		}
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return 0, true
	case corev1.PodFailed:
		return 1, true
	}
	return 0, false
}

func (c *containerHandlers) stop(w http.ResponseWriter, r *http.Request) {
	c.terminate(w, r, parseGrace(r.URL.Query().Get("t"), 10*time.Second))
}

func (c *containerHandlers) kill(w http.ResponseWriter, r *http.Request) {
	c.terminate(w, r, 0)
}

// delete drops a pending container from the store, or deletes the live pod.
// No-op if neither exists (lets `stop && rm` succeed).
func (c *containerHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if p := c.pending.getByRef(id); p != nil {
		p.markFailed(errPendingRemoved)
		c.pending.remove(p.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	pod, err := c.pods.FindByID(r.Context(), id)
	if errors.Is(err, k8s.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.deletePod(w, r, pod, 0)
}

// terminate (kill/stop) drops a pending entry or deletes the live pod.
// 404s only if neither exists.
func (c *containerHandlers) terminate(w http.ResponseWriter, r *http.Request, grace time.Duration) {
	id := r.PathValue("id")
	if p := c.pending.getByRef(id); p != nil {
		p.markFailed(errPendingRemoved)
		c.pending.remove(p.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	c.deletePod(w, r, pod, grace)
}

func (c *containerHandlers) deletePod(w http.ResponseWriter, r *http.Request, pod *corev1.Pod, grace time.Duration) {
	id := podspec.ContainerID(pod.Namespace, pod.Name)
	if err := c.registry.Close(id); err != nil {
		c.logger.Warn("close forwarder failed", "id", id, "err", err)
	}
	if err := c.pods.Delete(r.Context(), pod.Name, grace); err != nil && !errors.Is(err, k8s.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *containerHandlers) list(w http.ResponseWriter, r *http.Request) {
	pods, err := c.pods.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	all := boolQuery(r, "all")
	out := make([]dockerapi.ContainerSummary, 0, len(pods))
	for i := range pods {
		s := buildSummary(&pods[i])
		if !all && s.State != "running" {
			continue
		}
		out = append(out, s)
	}
	if all {
		for _, p := range c.pending.list() {
			out = append(out, summaryForPending(p))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	writeJSON(w, http.StatusOK, out)
}

func (c *containerHandlers) inspect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if p := c.pending.getByRef(id); p != nil {
		writeJSON(w, http.StatusOK, inspectForPending(p))
		return
	}
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildInspect(pod))
}

func (c *containerHandlers) logs(w http.ResponseWriter, r *http.Request) {
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	follow := boolQuery(r, "follow")
	tail, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64)

	rc, err := c.pods.StreamLogs(r.Context(), pod.Name, k8s.LogOptions{Follow: follow, TailLines: tail})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = rc.Close() }()

	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	bw := bufio.NewReader(rc)
	for {
		line, err := bw.ReadBytes('\n')
		if len(line) > 0 {
			if err := writeMultiplexedChunk(w, 1, line); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// resolvePod looks up the container by ID, writing 404 if absent.
func (c *containerHandlers) resolvePod(w http.ResponseWriter, r *http.Request) (*corev1.Pod, bool) {
	id := r.PathValue("id")
	pod, err := c.pods.FindByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, k8s.ErrNotFound) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("no such container: %s", id))
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return pod, true
}

// waitForContainerStarted polls until kubelet has started (or terminated) the
// container, so log streams can attach without the "ContainerCreating" error.
func (c *containerHandlers) waitForContainerStarted(ctx context.Context, name string) error {
	const poll = 200 * time.Millisecond
	for {
		pod, err := c.pods.Get(ctx, name)
		if err != nil {
			return err
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Running != nil || cs.State.Terminated != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// ensureRunning blocks on Ready, then opens forwarders. Idempotent.
// Tolerates ErrNotFound — a short-lived container may have already been
// cleaned up by /wait?condition=removed before we get here.
func (c *containerHandlers) ensureRunning(ctx context.Context, name string, mappings []podspec.PortMapping) error {
	if err := c.pods.WaitForReady(ctx, name); err != nil {
		if errors.Is(err, k8s.ErrNotFound) {
			return nil
		}
		return err
	}
	id := podspec.ContainerID(c.pods.Namespace(), name)
	if c.registry.Has(id) {
		return nil
	}
	fwMappings, err := toForwarderMappings(mappings)
	if err != nil {
		return err
	}
	if len(fwMappings) == 0 {
		return nil
	}
	h, err := c.forwarder.Open(ctx, c.pods.Namespace(), name, fwMappings)
	if err != nil {
		return fmt.Errorf("open forwarder: %w", err)
	}
	c.registry.Set(id, h)
	return nil
}

// loadMappingsFromPod decodes the ports annotation written at create time.
func loadMappingsFromPod(pod *corev1.Pod) ([]podspec.PortMapping, error) {
	raw := pod.Annotations[podspec.AnnotationPorts]
	if raw == "" {
		return nil, nil
	}
	var ports []podspec.PortMapping
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		return nil, fmt.Errorf("decode ports annotation: %w", err)
	}
	return ports, nil
}

func toForwarderMappings(ports []podspec.PortMapping) ([]forwarder.Mapping, error) {
	out := make([]forwarder.Mapping, 0, len(ports))
	for _, p := range ports {
		if p.HostPort == "" {
			continue
		}
		hp, err := strconv.ParseUint(p.HostPort, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid host port %q: %w", p.HostPort, err)
		}
		out = append(out, forwarder.Mapping{
			HostPort:      uint16(hp),
			ContainerPort: p.ContainerPort,
		})
	}
	return out, nil
}

// boolQuery: "1" or "true" → true. Matches Docker CLI's query-string convention.
func boolQuery(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "1" || strings.EqualFold(v, "true")
}

func parseGrace(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

// writeRawStreamResponse sends the hijacked-stream response headers. When the
// client requested `Connection: Upgrade, tcp` (docker CLI default for
// attach/exec start), we reply 101 Upgraded; otherwise 200 OK.
func writeRawStreamResponse(r *http.Request, brw *bufio.ReadWriter) error {
	status := "HTTP/1.1 200 OK\r\n"
	upgrade := ""
	if strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		status = "HTTP/1.1 101 UPGRADED\r\n"
		upgrade = "Connection: Upgrade\r\nUpgrade: " + r.Header.Get("Upgrade") + "\r\n"
	}
	_, err := brw.WriteString(status +
		"Content-Type: application/vnd.docker.raw-stream\r\n" +
		"Api-Version: " + APIVersion + "\r\n" +
		"Server: docker-in-kubernetes\r\n" +
		upgrade +
		"\r\n")
	if err != nil {
		return err
	}
	return brw.Flush()
}

// framedWriter serializes stdout/stderr writes from two goroutines into one
// hijacked conn, each chunk prefixed with a stdcopy header. Non-TTY only —
// for TTY, multiplexedStdoutStderr returns the raw writer (no framing).
type framedWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	stream byte
}

func (f *framedWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeMultiplexedChunk(f.w, f.stream, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func multiplexedStdoutStderr(w io.Writer, tty bool) (io.Writer, io.Writer) {
	if tty {
		return w, w
	}
	var mu sync.Mutex
	return &framedWriter{mu: &mu, w: w, stream: 1},
		&framedWriter{mu: &mu, w: w, stream: 2}
}

// writeMultiplexedChunk emits one Docker stdcopy frame (1B stream, 3B pad, 4B BE size).
func writeMultiplexedChunk(w io.Writer, stream byte, payload []byte) error {
	const maxChunk = 1 << 16
	chunks := payload
	for len(chunks) > 0 {
		n := len(chunks)
		if n > maxChunk {
			n = maxChunk
		}
		var hdr [8]byte
		hdr[0] = stream
		binary.BigEndian.PutUint32(hdr[4:], uint32(n))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(chunks[:n]); err != nil {
			return err
		}
		chunks = chunks[n:]
	}
	return nil
}
