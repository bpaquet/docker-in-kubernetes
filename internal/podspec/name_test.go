package podspec_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"redis", "redis"},
		{"MyApp", "myapp"},
		{"My_App", "my-app"},
		{"My__App", "my-app"},
		{"-leading-and-trailing-", "leading-and-trailing"},
		{"weird!chars@here#now", "weird-chars-here-now"},
		{strings.Repeat("a", 80), strings.Repeat("a", 63)},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := podspec.SanitizeName(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSanitizeNameEmptyReturnsRandom(t *testing.T) {
	a, err := podspec.SanitizeName("")
	require.NoError(t, err)
	b, err := podspec.SanitizeName("")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(a, "dink-"))
	assert.True(t, strings.HasPrefix(b, "dink-"))
	assert.NotEqual(t, a, b)
	assert.Len(t, a, len("dink-")+8)
}

func TestSanitizeNameAllInvalidChars(t *testing.T) {
	_, err := podspec.SanitizeName("!!!@@@###")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid characters")
}

func TestSanitizeNameTrimsTrailingDashAfterTruncation(t *testing.T) {
	// 62 'a's, dash, 1 'a' -> 64 chars total; truncate to 63 -> 'a'*62 + '-'
	// which must be trimmed.
	in := strings.Repeat("a", 62) + "-a"
	got, err := podspec.SanitizeName(in)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 62), got)
}

func TestGeneratedNameUsesImageBase(t *testing.T) {
	cases := []struct {
		image string
		want  string // expected prefix before the random suffix
	}{
		{"redis", "dink-redis-"},
		{"redis:7.2", "dink-redis-"},
		{"library/redis", "dink-redis-"},
		{"registry.example.com/library/redis:7.2", "dink-redis-"},
		{"redis@sha256:abc", "dink-redis-"},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			got := podspec.GeneratedName(tc.image)
			assert.True(t, strings.HasPrefix(got, tc.want), "got %q, want prefix %q", got, tc.want)
			assert.LessOrEqual(t, len(got), 63)
		})
	}
}
