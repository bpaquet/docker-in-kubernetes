package forwarder_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/forwarder"
)

type fakeResolver struct {
	ip  string
	err error
}

func (f fakeResolver) PodIP(_ context.Context, _, _ string) (string, error) {
	return f.ip, f.err
}

// startEcho returns an echo TCP server that closes when t completes.
func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := uint16(ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, ln.Close())
	return p
}

func TestTCPForwarderProxiesBytes(t *testing.T) {
	echo := startEcho(t)
	target := echo.Addr().(*net.TCPAddr)

	hostPort := freePort(t)
	fw := forwarder.NewTCPForwarder(fakeResolver{ip: "127.0.0.1"}, nil)
	h, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{
		{HostPort: hostPort, ContainerPort: uint16(target.Port)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(hostPort))), time.Second)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	buf := make([]byte, 5)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}

func TestTCPForwarderCloseStopsAccepting(t *testing.T) {
	echo := startEcho(t)
	target := echo.Addr().(*net.TCPAddr)

	hostPort := freePort(t)
	fw := forwarder.NewTCPForwarder(fakeResolver{ip: "127.0.0.1"}, nil)
	h, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{
		{HostPort: hostPort, ContainerPort: uint16(target.Port)},
	})
	require.NoError(t, err)

	require.NoError(t, h.Close())

	_, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(hostPort))), 200*time.Millisecond)
	require.Error(t, err)
}

func TestTCPForwarderResolverError(t *testing.T) {
	fw := forwarder.NewTCPForwarder(fakeResolver{err: errors.New("nope")}, nil)
	_, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{{HostPort: 1, ContainerPort: 1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestTCPForwarderEmptyPodIP(t *testing.T) {
	fw := forwarder.NewTCPForwarder(fakeResolver{ip: ""}, nil)
	_, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{{HostPort: 1, ContainerPort: 1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PodIP")
}

func TestTCPForwarderSkipsMappingsWithoutHostPort(t *testing.T) {
	fw := forwarder.NewTCPForwarder(fakeResolver{ip: "127.0.0.1"}, nil)
	h, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{{ContainerPort: 80}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
}

func TestTCPForwarderHostPortAlreadyBound(t *testing.T) {
	taken := startEcho(t)
	port := uint16(taken.Addr().(*net.TCPAddr).Port)

	fw := forwarder.NewTCPForwarder(fakeResolver{ip: "127.0.0.1"}, nil)
	_, err := fw.Open(t.Context(), "ns", "pod", []forwarder.Mapping{
		{HostPort: port, ContainerPort: port},
	})
	require.Error(t, err)
}
