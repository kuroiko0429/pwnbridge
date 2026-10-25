package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// Observe entry to the real socket Write without substituting Emit or SendMessage.
type observedBusSocket struct {
	net.Conn
	once    sync.Once
	writing chan struct{}
}

func (c *observedBusSocket) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(b)
}

func TestBusBackpressureDoesNotBlockRuntimeAndCancellation(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	wire := &observedBusSocket{Conn: socket, writing: make(chan struct{})}
	conn, err := dbus.NewConn(wire)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the raw socket first is essential: godbus Close waits for send's read lock.
	defer func() { socket.Close(); conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRuntime(goodConfig())
	// Exercise the notification installation independently of bus name acquisition.
	p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
	now := time.Now()
	applied := make(chan struct{})
	go func() {
		r.apply(func(e *Engine) []Event { return e.Begin("usb", 1, now) })
		r.apply(func(e *Engine) []Event { return e.Frame("usb", 1, snap("a"), now) })
		close(applied)
	}()
	select {
	case <-wire.writing:
	case <-time.After(time.Second):
		t.Fatal("no real socket write")
	}
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("publication blocked runtime mutex")
	}
	checked := make(chan State, 1)
	failedOver := make(chan State, 1)
	go func() {
		r.apply(func(e *Engine) []Event { return e.Begin("bluetooth", 1, now) })
		r.apply(func(e *Engine) []Event { return e.Frame("bluetooth", 1, snap("a"), now) })
		r.apply(func(e *Engine) []Event {
			events := e.Fail("usb", 1, "test link lost", now)
			failedOver <- e.State()
			return events
		})
		var state State
		r.apply(func(e *Engine) []Event {
			events := e.Tick(now.Add(r.config.StaleAfter.Duration))
			state = e.State()
			return events
		})
		r.Snapshot()
		checked <- state
	}()
	select {
	case s := <-checked:
		if s.Connected || !s.Stale {
			t.Fatal("freshness stopped", s)
		}
	case <-time.After(time.Second):
		t.Fatal("Snapshot/freshness blocked by non-reading bus")
	}
	if s := <-failedOver; !s.Connected || s.Transport != "bluetooth" {
		t.Fatal("failover stopped", s)
	}
	cancel()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("publisher cancellation blocked")
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("bus not closed")
	}
}

func TestBusPublisherPreservesWireOrder(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	conn, err := dbus.NewConn(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRuntime(goodConfig())
	p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
	defer func() { cancel(); <-p.done }()
	events := []Event{{Type: "connected"}, {Type: "device_restarted"}}
	r.mu.Lock()
	r.notify(State{Transport: "usb"}, events)
	events[0].Type = "modified-after-enqueue"
	r.notify(State{Transport: "bluetooth"}, []Event{{Type: "transport_changed"}})
	r.mu.Unlock()
	for _, want := range []struct{ member, value string }{
		{"PropertiesChanged", "usb"}, {"Event", "connected"}, {"Event", "device_restarted"},
		{"PropertiesChanged", "bluetooth"}, {"Event", "transport_changed"},
	} {
		msg := readBusMessage(t, peer)
		if got := msg.Headers[dbus.FieldMember].Value(); got != want.member {
			t.Fatalf("member=%v want %s", got, want.member)
		}
		if want.member == "Event" {
			if got := eventType(t, msg); got != want.value {
				t.Fatalf("event=%s want %s", got, want.value)
			}
		} else {
			properties := msg.Body[1].(map[string]dbus.Variant)
			if got := properties["Transport"].Value(); got != want.value {
				t.Fatalf("transport=%v want %s", got, want.value)
			}
		}
	}
}

func TestBusPublisherOverflowFailsDaemon(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	wire := &observedBusSocket{Conn: socket, writing: make(chan struct{})}
	conn, err := dbus.NewConn(wire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRuntime(goodConfig())
	r.gate = func(Endpoint) (Binding, error) { return Binding{}, errors.New("test endpoint unavailable") }
	p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
	defer func() { cancel(); <-p.done }()
	r.Snapshot()
	select {
	case <-wire.writing:
	case <-time.After(time.Second):
		t.Fatal("no blocked write")
	}
	r.mu.Lock()
	for i := 0; i < busNotificationCapacity+1; i++ {
		r.notify(r.stateLocked(), []Event{{Type: "connected"}})
	}
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- runDaemonRuntime(context.Background(), ctx, cancel, r, p) }()
	select {
	case err := <-done:
		if !errors.Is(err, errBusNotificationOverflow) {
			t.Fatalf("overflow did not fail daemon: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow failed to stop daemon")
	}
	if conn.Context().Err() == nil {
		t.Fatal("overflow kept bus alive")
	}
}

func TestBusPublisherSendErrorsFailDaemon(t *testing.T) {
	for _, phase := range []string{"PropertiesChanged", "Event"} {
		t.Run(phase, func(t *testing.T) {
			socket, peer := net.Pipe()
			defer peer.Close()
			conn, err := dbus.NewConn(socket)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { socket.Close(); conn.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := NewRuntime(goodConfig())
			r.gate = func(Endpoint) (Binding, error) { return Binding{}, errors.New("test endpoint unavailable") }
			p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
			defer func() { cancel(); <-p.done }()
			r.apply(func(e *Engine) []Event { return []Event{{Type: "connected"}} })
			if phase == "Event" {
				readBusMessage(t, peer)
			}
			peer.Close()
			done := make(chan error, 1)
			go func() { done <- runDaemonRuntime(context.Background(), ctx, cancel, r, p) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), phase) {
					t.Fatalf("send failure was not propagated: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("send error failed to stop daemon")
			}
		})
	}
}

func TestBusPublisherStalledWriteTimesOut(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	conn, err := dbus.NewConn(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRuntime(goodConfig())
	p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
	r.Snapshot()
	select {
	case <-p.done:
		if p.err == nil || !strings.Contains(p.err.Error(), "timed out") {
			t.Fatalf("no publication timeout: %v", p.err)
		}
	case <-time.After(busPublicationTimeout + time.Second):
		t.Fatal("stalled publication unbounded")
	}
}

func TestBusPublisherCancellationStopsDaemonRuntime(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	wire := &observedBusSocket{Conn: socket, writing: make(chan struct{})}
	conn, err := dbus.NewConn(wire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); conn.Close() }()
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	child, cancel := context.WithCancel(parent)
	defer cancel()
	r := NewRuntime(goodConfig())
	r.gate = func(Endpoint) (Binding, error) { return Binding{}, errors.New("test endpoint unavailable") }
	p := startBusPublisher(conn, r, child, func() { socket.Close() })
	defer func() { cancel(); <-p.done }()
	done := make(chan error, 1)
	go func() { done <- runDaemonRuntime(parent, child, cancel, r, p) }()
	select {
	case <-wire.writing:
	case <-time.After(time.Second):
		t.Fatal("no blocked write")
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon/runtime cancellation deadlocked on socket publication")
	}
}

func TestBusPublisherAlreadyCancelledDoesNotSend(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	wire := &observedBusSocket{Conn: socket, writing: make(chan struct{})}
	conn, err := dbus.NewConn(wire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRuntime(goodConfig())
	p := startBusPublisher(conn, r, ctx, func() { socket.Close() })
	r.Snapshot()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("pre-cancelled publisher did not stop")
	}
	if p.err != nil {
		t.Fatal(p.err)
	}
	select {
	case <-wire.writing:
		t.Fatal("sent after cancellation")
	default:
	}
	// Late state updates must remain nonblocking after shutdown as well.
	for i := 0; i < busNotificationCapacity+1; i++ {
		r.Reconnect()
	}
}

// Decode the actual wire messages, not an Emit mock.
func readBusMessage(t *testing.T, peer net.Conn) *dbus.Message {
	t.Helper()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	msg, err := dbus.DecodeMessage(peer)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func eventType(t *testing.T, msg *dbus.Message) string {
	t.Helper()
	var event Event
	if err := json.Unmarshal([]byte(msg.Body[0].(string)), &event); err != nil {
		t.Fatal(err)
	}
	return event.Type
}
