package k8s_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/k8s"
)

const fakeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: deadbeef
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, []byte(fakeKubeconfig), 0o600))
	return path
}

func TestConnectLocalMode(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	conn, err := k8s.Connect(k8s.ClientConfig{KubeconfigPath: writeKubeconfig(t)})
	require.NoError(t, err)
	assert.NotNil(t, conn.Clientset)
	assert.NotNil(t, conn.REST)
	assert.Equal(t, k8s.ModeLocal, conn.Mode)
}

func TestConnectInClusterMissingSAFails(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "6443")
	_, err := k8s.Connect(k8s.ClientConfig{})
	require.Error(t, err)
}

func TestModeString(t *testing.T) {
	assert.Equal(t, "local", k8s.ModeLocal.String())
	assert.Equal(t, "in-cluster", k8s.ModeInCluster.String())
}
