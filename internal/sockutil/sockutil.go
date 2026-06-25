// Package sockutil opens the daemon's UNIX socket.
package sockutil

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// ListenUnix binds a UNIX socket at path with mode 0600, replacing any stale
// socket. Refuses to remove a non-socket file at path.
func ListenUnix(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to remove non-socket file at %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat socket path: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return listener, nil
}
