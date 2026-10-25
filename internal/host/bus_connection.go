package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// The normal-user daemon owns the socket independently of godbus. Never use
// godbus Close as the interrupt operation: it waits for an in-flight write.
// Session buses on this Linux host use unix:path or unix:abstract addresses.
func connectDaemonBus(ctx context.Context) (*dbus.Conn, *net.UnixConn, error) {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			dir = fmt.Sprintf("/run/user/%d", os.Getuid())
		}
		address = "unix:path=" + dbus.EscapeBusAddressValue(filepath.Join(dir, "bus"))
	}
	var last error
	for _, entry := range strings.Split(address, ";") {
		keys, ok := strings.CutPrefix(entry, "unix:")
		if !ok {
			last = errors.New("daemon requires a Unix session D-Bus address")
			continue
		}
		path := ""
		for _, field := range strings.Split(keys, ",") {
			key, value, ok := strings.Cut(field, "=")
			if !ok || (key != "path" && key != "abstract") {
				continue
			}
			decoded, err := dbus.UnescapeBusAddressValue(value)
			if err != nil {
				last = err
				continue
			}
			path = decoded
			if key == "abstract" {
				path = "@" + decoded
			}
			break
		}
		if path == "" {
			last = errors.New("Unix session D-Bus address has no path or abstract socket")
			continue
		}
		raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
		if err != nil {
			last = err
			continue
		}
		socket := raw.(*net.UnixConn)
		// Bounds authentication, Hello, and initial name acquisition too. The caller
		// clears this startup deadline only after ExportBus has completed.
		socket.SetDeadline(time.Now().Add(5 * time.Second))
		conn, err := dbus.DialUnix(socket)
		if err != nil {
			socket.Close()
			return nil, nil, err
		}
		go func() {
			select {
			case <-ctx.Done():
			case <-conn.Context().Done():
			}
			socket.Close()
		}()
		if err = conn.Auth(nil); err == nil {
			err = conn.Hello()
		}
		if err != nil {
			socket.Close()
			conn.Close()
			last = err
			continue
		}
		return conn, socket, nil
	}
	if last == nil {
		last = errors.New("empty session D-Bus address")
	}
	return nil, nil, last
}
