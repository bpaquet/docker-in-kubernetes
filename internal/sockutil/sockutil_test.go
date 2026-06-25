package sockutil_test

import (
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/sockutil"
)

// shortTempPath returns a UNIX socket path that fits inside the platform's
// sun_path limit (104 bytes on darwin). t.TempDir() on darwin uses the long
// /var/folders/... prefix, so tests chdir into it and use a short basename.
func shortTempPath(t *testing.T, name string) string {
	t.Helper()
	t.Chdir(t.TempDir())
	return name
}

func TestListenUnixCreatesSocketWithMode0600(t *testing.T) {
	path := shortTempPath(t, "s.sock")

	listener, err := sockutil.ListenUnix(path)
	require.NoError(t, err)
	defer listener.Close()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.True(t, info.Mode()&os.ModeSocket != 0, "expected file to be a socket")
}

func TestListenUnixRemovesStaleSocket(t *testing.T) {
	path := shortTempPath(t, "s.sock")

	// Leave a stale socket file behind: bind, disable unlink-on-close, close.
	stale, err := net.Listen("unix", path)
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())

	info, err := os.Stat(path)
	require.NoError(t, err, "stale socket file must exist before the second ListenUnix")
	require.True(t, info.Mode()&os.ModeSocket != 0)

	listener, err := sockutil.ListenUnix(path)
	require.NoError(t, err)
	defer listener.Close()
}

func TestListenUnixRefusesToRemoveRegularFile(t *testing.T) {
	path := shortTempPath(t, "regular")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))

	_, err := sockutil.ListenUnix(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to remove non-socket file")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestListenUnixHandlesNonExistentPath(t *testing.T) {
	path := shortTempPath(t, "fresh.sock")

	listener, err := sockutil.ListenUnix(path)
	require.NoError(t, err)
	defer listener.Close()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSocket != 0)
}
