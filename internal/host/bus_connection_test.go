package host

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestBusAuthCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+dbus.EscapeBusAddressValue(path))
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := listener.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, socket, err := connectDaemonBus(ctx)
		if conn != nil {
			socket.Close()
			conn.Close()
		}
		done <- err
	}()
	select {
	case peer := <-accepted:
		defer peer.Close()
	case <-time.After(time.Second):
		t.Fatal("no socket connection")
	}
	// The accepted actual peer never participates in authentication or reads.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("authenticated a non-reading peer")
		}
	case <-time.After(time.Second):
		t.Fatal("startup cancellation failed to interrupt authentication")
	}
}
