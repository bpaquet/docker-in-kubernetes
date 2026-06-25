package logutil_test

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/logutil"
)

func TestNewParsesValidLevels(t *testing.T) {
	cases := []struct {
		input string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"INFO", slog.LevelInfo},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			logger, err := logutil.New(io.Discard, c.input)
			require.NoError(t, err)
			require.NotNil(t, logger)
			assert.True(t, logger.Enabled(t.Context(), c.level))
		})
	}
}

func TestNewRejectsInvalidLevel(t *testing.T) {
	_, err := logutil.New(io.Discard, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid log level")
}

func TestNewLevelFiltersBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logutil.New(&buf, "warn")
	require.NoError(t, err)

	logger.Info("should not appear")
	logger.Warn("should appear")

	out := buf.String()
	assert.NotContains(t, out, "should not appear")
	assert.Contains(t, out, "should appear")
}
