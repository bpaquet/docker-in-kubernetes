package server

import (
	"net/http"
	"strconv"
	"time"
)

// handleEvents holds the connection open and emits nothing — mirrors a quiet
// daemon so compose stays subscribed for the life of `compose up` without
// reconnect loops. The `until` query param is honored so `docker events
// --until=…` exits cleanly; the CLI sends it as a unix timestamp (seconds,
// possibly fractional) or an RFC3339 string.
//
// Flush via NewResponseController: logRequests wraps the writer in a
// statusRecorder that doesn't implement Flusher itself.
func handleEvents(w http.ResponseWriter, r *http.Request) {
	setDockerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()

	var deadline <-chan time.Time
	if t, ok := parseEventTime(r.URL.Query().Get("until")); ok {
		d := time.Until(t)
		if d <= 0 {
			return
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		deadline = timer.C
	}

	select {
	case <-r.Context().Done():
	case <-deadline:
	}
}

// Try RFC3339 first so a bare year ("2026") isn't parsed as unix seconds.
func parseEventTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec, frac := int64(f), f-float64(int64(f))
		return time.Unix(sec, int64(frac*1e9)), true
	}
	return time.Time{}, false
}
