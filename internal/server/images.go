package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/images"
)

// imageHandlers serves the /images endpoints. We don't actually pull —
// cluster pull-on-create handles the real work. The store exists so the
// CLI's image workflow succeeds end-to-end against the recorded refs.
type imageHandlers struct {
	store *images.Store
	now   func() time.Time
}

func (h *imageHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /images/create", h.create)
	mux.HandleFunc("GET /images/json", h.list)
	// Catch-all because image names contain '/' (e.g. ghcr.io/foo/bar) and
	// stdlib mux disallows segments after a {x...} wildcard.
	mux.HandleFunc("GET /images/{path...}", h.routeGet)
	mux.HandleFunc("DELETE /images/{path...}", h.routeDelete)
}

// routeGet dispatches GET /images/<name>/json → inspect.
func (h *imageHandlers) routeGet(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	name, ok := strings.CutSuffix(path, "/json")
	if !ok || name == "" {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	h.inspect(w, r, name)
}

func (h *imageHandlers) routeDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	if name == "" {
		writeError(w, http.StatusBadRequest, "image name required")
		return
	}
	h.delete(w, r, name)
}

// create handles `docker pull`. Records the ref and streams a couple of
// fake jsonmessage lines — enough for the CLI to print a success and exit 0.
func (h *imageHandlers) create(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromImage := q.Get("fromImage")
	tag := q.Get("tag")
	if fromImage == "" {
		writeError(w, http.StatusBadRequest, "fromImage is required")
		return
	}
	ref := buildRef(fromImage, tag)
	h.store.Record(ref, tag, h.now())

	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	emit := func(msg map[string]string) {
		_ = enc.Encode(msg)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit(map[string]string{"status": "Pulling from " + fromImage, "id": displayTag(tag)})
	emit(map[string]string{"status": "Status: Image is up to date for " + ref})
}

func (h *imageHandlers) list(w http.ResponseWriter, _ *http.Request) {
	records := h.store.List()
	out := make([]dockerapi.ImageSummary, 0, len(records))
	for _, r := range records {
		out = append(out, dockerapi.ImageSummary{
			ID:          r.ID(),
			RepoTags:    []string{r.Ref},
			RepoDigests: []string{},
			Created:     r.PulledAt.Unix(),
			Labels:      map[string]string{},
			Containers:  -1,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *imageHandlers) inspect(w http.ResponseWriter, _ *http.Request, name string) {
	rec, ok := h.store.Find(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No such image: %s", name))
		return
	}
	writeJSON(w, http.StatusOK, dockerapi.ImageInspect{
		ID:           rec.ID(),
		RepoTags:     []string{rec.Ref},
		RepoDigests:  []string{},
		Created:      rec.PulledAt.Format(time.RFC3339Nano),
		Architecture: runtime.GOARCH,
		Os:           "linux",
	})
}

func (h *imageHandlers) delete(w http.ResponseWriter, _ *http.Request, name string) {
	rec, ok := h.store.Remove(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No such image: %s", name))
		return
	}
	writeJSON(w, http.StatusOK, []dockerapi.ImageDeleteItem{
		{Untagged: rec.Ref},
		{Deleted: rec.ID()},
	})
}

// buildRef joins fromImage and tag the way the docker CLI sends them. If
// fromImage already carries a digest (`@sha256:...`), tag is ignored.
func buildRef(fromImage, tag string) string {
	if strings.Contains(fromImage, "@") {
		return fromImage
	}
	if tag == "" {
		return fromImage + ":latest"
	}
	return fromImage + ":" + tag
}

func displayTag(tag string) string {
	if tag == "" {
		return "latest"
	}
	return tag
}
