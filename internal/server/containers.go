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

// attach hijacks the HTTP connection. With stdin we run a live SPDY attach.
// Without stdin we tail the pod's logs — that path also captures output for
// containers that exit before the CLI attaches.
func (c *containerHandlers) attach(w http.ResponseWriter, r *http.Request) {
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	wantStdin := boolQuery(r, "stdin")
	tty := len(pod.Spec.Containers) > 0 && pod.Spec.Containers[0].TTY

	conn, brw, err := hijack(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	if err := writeRawStreamResponse(r, brw); err != nil {
		c.logger.Warn("attach: hijack write failed", "err", err)
		return
	}

	if wantStdin {
		c.attachInteractive(pod, conn, brw, tty)
		return
	}
	c.attachLogs(pod, conn, tty)
}

func (c *containerHandlers) attachInteractive(pod *corev1.Pod, conn net.Conn, brw *bufio.ReadWriter, tty bool) {
	stdout, stderr := multiplexedStdoutStderr(conn, tty)
	// Prefer raw conn for stdin: bufio.Reader can buffer bytes the apiserver
	// SPDY layer can't see. brw.Reader is only needed when the server already
	// pre-buffered post-header bytes (e.g. a request body before upgrade) —
	// /attach has no body, so conn is safe.
	opts := k8s.StreamOptions{Stdin: conn, Stdout: stdout, TTY: tty}
	if !tty {
		opts.Stderr = stderr
	}
	_ = brw // keep param so unused-import / unused-write lints stay quiet
	c.logger.Info("attach: SPDY attach starting", "container", pod.Name, "tty", tty)
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

// create posts the pod and waits for Ready. /start is then a no-op.
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

	if _, err := c.pods.Create(r.Context(), built.Pod); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := c.ensureRunning(r.Context(), built.PodName, built.PortMappings); err != nil {
		c.logger.Warn("ensureRunning failed", "name", built.PodName, "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	id := podspec.ContainerID(c.pods.Namespace(), built.PodName)
	writeJSON(w, http.StatusCreated, dockerapi.CreateResponse{ID: id, Warnings: []string{}})
}

// start: idempotent. 204 even on missing pod, so docker run --rm doesn't see
// "no such container" when /wait?condition=removed wins the race.
func (c *containerHandlers) start(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pod, err := c.pods.FindByID(r.Context(), id)
	if errors.Is(err, k8s.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
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

// wait short-circuits condition=next-exit while the container is still
// running (see Design.md "Wait endpoint quirk"). condition=removed blocks
// until exit and then deletes the pod — the basis of `docker run --rm`.
func (c *containerHandlers) wait(w http.ResponseWriter, r *http.Request) {
	pod, ok := c.resolvePod(w, r)
	if !ok {
		return
	}
	condition := r.URL.Query().Get("condition")
	if condition == "next-exit" {
		if exit, done := exitCodeFromPod(pod); done {
			writeJSON(w, http.StatusOK, dockerapi.WaitResponse{StatusCode: exit})
			return
		}
		writeJSON(w, http.StatusOK, dockerapi.WaitResponse{StatusCode: 0})
		return
	}
	exit, err := c.waitForExit(r.Context(), pod.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if condition == "removed" {
		_ = c.registry.Close(podspec.ContainerID(pod.Namespace, pod.Name))
		_ = c.pods.Delete(r.Context(), pod.Name, 0)
	}
	writeJSON(w, http.StatusOK, dockerapi.WaitResponse{StatusCode: exit})
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

func exitCodeFromPod(pod *corev1.Pod) (int64, bool) {
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return 0, true
	case corev1.PodFailed:
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil {
				return int64(cs.State.Terminated.ExitCode), true
			}
		}
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

// delete is a no-op if the pod is already gone (lets `stop && rm` succeed).
func (c *containerHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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

// terminate (kill/stop) 404s on a missing pod, unlike delete.
func (c *containerHandlers) terminate(w http.ResponseWriter, r *http.Request, grace time.Duration) {
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
	// Stable order: most-recently-created first.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	writeJSON(w, http.StatusOK, out)
}

func (c *containerHandlers) inspect(w http.ResponseWriter, r *http.Request) {
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

// ensureRunning blocks on Ready, then opens forwarders. Idempotent.
func (c *containerHandlers) ensureRunning(ctx context.Context, name string, mappings []podspec.PortMapping) error {
	if err := c.pods.WaitForReady(ctx, name); err != nil {
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
