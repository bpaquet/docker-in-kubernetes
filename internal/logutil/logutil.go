// Package logutil constructs the daemon's slog.Logger.
package logutil

import (
	"fmt"
	"io"
	"log/slog"
)

// New returns a text-handler slog.Logger writing to w at the given level.
func New(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
