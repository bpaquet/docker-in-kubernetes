//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

// TestDockerPsEmpty confirms the daemon answers /containers/json with an
// empty list when no managed pods exist (and the docker CLI shows only the
// header row).
func TestDockerPsEmpty(t *testing.T) {
	env := newEnv(t)
	out, err := env.docker(t, 15*time.Second, "ps")
	require.NoError(t, err, "docker ps failed:\n%s", out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "expected only the header row, got:\n%s", out)
	assert.Contains(t, lines[0], "CONTAINER ID")
}

// TestDockerPsListsManagedPod creates a managed pod directly via the k8s API
// (bypassing /containers/create, so the forwarder is NOT involved) and checks
// `docker ps -a` includes it.
func TestDockerPsListsManagedPod(t *testing.T) {
	env := newEnv(t)

	name := "it-ps-" + randSuffix()
	cleanupPod(t, env.Pods, name)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				podspec.LabelManaged:       "true",
				podspec.LabelContainerName: name,
				podspec.LabelProject:       podspec.DefaultProject,
			},
			Annotations: map[string]string{
				podspec.AnnotationImage:      "alpine:3",
				podspec.AnnotationDockerName: name,
				podspec.AnnotationCreatedAt:  time.Now().UTC().Format(time.RFC3339),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{Name: "main", Image: "alpine:3", Command: []string{"sleep", "300"}},
			},
		},
	}
	_, err := env.Pods.Create(t.Context(), pod)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		out, err := env.docker(t, 10*time.Second, "ps", "-a")
		if err != nil {
			t.Logf("docker ps -a transient error: %v\n%s", err, out)
			return false
		}
		return strings.Contains(out, name)
	}, 30*time.Second, 500*time.Millisecond, "docker ps -a should eventually list %s", name)
}
