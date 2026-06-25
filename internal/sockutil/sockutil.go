// Package sockutil contains helpers for binding the daemon's UNIX socket.
package sockutil

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// ListenUnix opens a UNIX socket at path with mode 0600.
//
// If path exists as a stale socket file, it is removed before binding. If
// path exists as any other kind of file, ListenUnix refuses to remove it and
// returns an error.
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
