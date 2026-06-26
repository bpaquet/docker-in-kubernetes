//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerPullRecordsImage(t *testing.T) {
	env := newEnv(t)

	out, err := env.docker(t, 30*time.Second, "pull", "alpine:3")
	require.NoError(t, err, "docker pull failed:\n%s", out)
	assert.Contains(t, out, "Status: Image is up to date for alpine:3")

	out, err = env.docker(t, 10*time.Second, "images")
	require.NoError(t, err, "docker images failed:\n%s", out)
	assert.Contains(t, out, "alpine")
	// header + one entry
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2, "expected one image row, got:\n%s", out)
	assert.Contains(t, lines[0], "REPOSITORY")
}

func TestDockerImagesEmpty(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 10*time.Second, "images")
	require.NoError(t, err, "docker images failed:\n%s", out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "expected only header row, got:\n%s", out)
	assert.Contains(t, lines[0], "REPOSITORY")
}

func TestDockerImageInspect(t *testing.T) {
	env := newEnv(t)

	_, err := env.docker(t, 30*time.Second, "pull", "redis:7")
	require.NoError(t, err)

	out, err := env.docker(t, 10*time.Second, "image", "inspect", "redis:7", "--format", "{{.Id}} {{.RepoTags}} {{.Os}}")
	require.NoError(t, err, "docker image inspect failed:\n%s", out)
	assert.Contains(t, out, "sha256:")
	assert.Contains(t, out, "redis:7")
	assert.Contains(t, out, "linux")
}

func TestDockerImageInspectMissing(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 10*time.Second, "image", "inspect", "ghost:tag")
	require.Error(t, err, "expected non-zero exit for unknown image, got output:\n%s", out)
	assert.Contains(t, out, "No such image")
}

func TestDockerImageRm(t *testing.T) {
	env := newEnv(t)

	_, err := env.docker(t, 30*time.Second, "pull", "busybox:latest")
	require.NoError(t, err)

	out, err := env.docker(t, 10*time.Second, "image", "rm", "busybox:latest")
	require.NoError(t, err, "docker image rm failed:\n%s", out)
	assert.Contains(t, out, "Untagged: busybox:latest")
	assert.Contains(t, out, "Deleted: sha256:")

	listOut, err := env.docker(t, 10*time.Second, "images")
	require.NoError(t, err)
	assert.NotContains(t, listOut, "busybox", "image should be gone from `docker images`:\n%s", listOut)
}

func TestDockerRmiMissing(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 10*time.Second, "rmi", "ghost:tag")
	require.Error(t, err, "expected non-zero exit for unknown image, got output:\n%s", out)
	assert.Contains(t, out, "No such image")
}

func TestDockerPullDefaultsToLatest(t *testing.T) {
	env := newEnv(t)

	_, err := env.docker(t, 30*time.Second, "pull", "alpine")
	require.NoError(t, err)

	out, err := env.docker(t, 10*time.Second, "image", "inspect", "alpine", "--format", "{{index .RepoTags 0}}")
	require.NoError(t, err, "docker image inspect failed:\n%s", out)
	assert.Contains(t, out, "alpine:latest")
}
