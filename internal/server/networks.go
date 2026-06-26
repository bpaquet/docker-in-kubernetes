package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/networks"
)

type networkHandlers struct {
	store *networks.Store
	now   func() time.Time
}

func (h *networkHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /networks/create", h.create)
	mux.HandleFunc("GET /networks", h.list)
	mux.HandleFunc("GET /networks/{name}", h.inspect)
	mux.HandleFunc("DELETE /networks/{name}", h.delete)
	mux.HandleFunc("POST /networks/{name}/connect", h.noop)
	mux.HandleFunc("POST /networks/{name}/disconnect", h.noop)
}

func (h *networkHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req dockerapi.NetworkCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	if h.store.Has(req.Name) {
		writeError(w, http.StatusConflict, fmt.Sprintf("network with name %s already exists", req.Name))
		return
	}
	rec := h.store.Record(req.Name, req.Driver, req.Labels, h.now())
	writeJSON(w, http.StatusCreated, dockerapi.NetworkCreateResponse{ID: rec.ID()})
}

func (h *networkHandlers) list(w http.ResponseWriter, _ *http.Request) {
	records := h.store.List()
	out := make([]dockerapi.Network, 0, len(records))
	for _, r := range records {
		out = append(out, toNetwork(r))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *networkHandlers) inspect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rec, ok := h.store.Find(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("network %s not found", name))
		return
	}
	writeJSON(w, http.StatusOK, toNetwork(rec))
}

func (h *networkHandlers) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.store.Remove(name); !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("network %s not found", name))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// noop: pods in the same namespace already reach each other; nothing to wire.
// Body is drained so keep-alive can reuse the connection.
func (h *networkHandlers) noop(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
}

func toNetwork(r networks.Record) dockerapi.Network {
	labels := r.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return dockerapi.Network{
		Name:       r.Name,
		ID:         r.ID(),
		Created:    r.CreatedAt.Format(time.RFC3339Nano),
		Scope:      "local",
		Driver:     r.Driver,
		IPAM:       dockerapi.NetworkIPAM{Driver: "default", Options: map[string]string{}, Config: []map[string]string{}},
		Containers: map[string]dockerapi.NetworkContainer{},
		Options:    map[string]string{},
		Labels:     labels,
	}
}
