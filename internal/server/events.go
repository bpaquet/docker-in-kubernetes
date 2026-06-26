package server

import "net/http"

// handleEvents holds the connection open and emits nothing — mirrors a quiet
// daemon so compose stays subscribed for the life of `compose up` without
// reconnect loops. NewResponseController.Flush is required: logRequests wraps
// the writer in a statusRecorder that doesn't implement Flusher itself.
func handleEvents(w http.ResponseWriter, r *http.Request) {
	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
	<-r.Context().Done()
}
