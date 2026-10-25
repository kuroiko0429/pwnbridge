package host

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/godbus/dbus/v5"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every integration gets a private bus, including when go test runs inside a live desktop session.
func isolatedBus(t *testing.T) bool {
	t.Helper()
	if os.Getenv("PWNBRIDGE_PRIVATE_TEST_BUS") == "1" {
		return true
	}
	if _, err := exec.LookPath("dbus-run-session"); err != nil {
		t.Skip("dbus-run-session unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dbus-run-session", "--", os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "PWNBRIDGE_PRIVATE_TEST_BUS=1", "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private session test: %v\n%s", err, out)
	}
	t.Log(string(out))
	return false
}

type lineWriter struct{ lines chan string }

func (w lineWriter) Write(b []byte) (int, error) { w.lines <- string(b); return len(b), nil }
func TestCLIWatchStateAndEvents(t *testing.T) {
	if !isolatedBus(t) {
		return
	}
	server, socket, err := connectDaemonBus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); server.Close() }()
	r := NewRuntime(goodConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := startBusPublisher(server, r, ctx, func() { socket.Close() })
	defer func() { cancel(); <-p.done }()
	if err = ExportBus(server, r, ctx); err != nil {
		t.Fatal(err)
	}
	writer := lineWriter{make(chan string, 20)}
	done := make(chan error, 1)
	go func() { done <- RunCLI(ctx, []string{"watch", "--json"}, writer, writer) }()
	select {
	case first := <-writer.lines:
		if !strings.Contains(first, `"type":"state"`) {
			t.Fatal(first)
		}
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("watch did not start")
	}
	now := time.Now()
	r.apply(func(e *Engine) []Event { return e.Begin("usb", 1, now) })
	r.apply(func(e *Engine) []Event { return e.Frame("usb", 1, snap("a"), now) })
	state, event := false, false
	deadline := time.After(time.Second)
	for !state || !event {
		select {
		case line := <-writer.lines:
			state = state || strings.Contains(line, `"connected":true`)
			event = event || strings.Contains(line, `"type":"connected"`)
		case <-deadline:
			t.Fatal("watch missing state or event", state, event)
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCLIDaemonSessionLifecycle(t *testing.T) {
	if !isolatedBus(t) {
		return
	}
	c := goodConfig()
	c.Endpoints[0].Interface = "pwnbridge-test-none"
	data, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := RunCLI(ctx, []string{"daemon", "--config", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
}
func TestSessionDBusStatePropertiesSignalsAndErrors(t *testing.T) {
	if !isolatedBus(t) {
		return
	}
	server, socket, err := connectDaemonBus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); server.Close() }()
	client, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	r := NewRuntime(goodConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := startBusPublisher(server, r, ctx, func() { socket.Close() })
	defer func() { cancel(); <-p.done }()
	if err := ExportBus(server, r, ctx); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunCLI(ctx, []string{"status", "--json"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"stale":true`) {
		t.Fatal(output.String())
	}
	if err := RunCLI(ctx, []string{"reconnect"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	obj := client.Object(BusName, BusPath)
	var raw string
	if err = obj.Call(BusInterface+".GetState", 0).Store(&raw); err != nil {
		t.Fatal(err)
	}
	var s State
	if err = json.Unmarshal([]byte(raw), &s); err != nil || s.Connected || !s.Stale {
		t.Fatal(raw, err)
	}
	var v dbus.Variant
	if err = obj.Call("org.freedesktop.DBus.Properties.Get", 0, BusInterface, "CPUTemperatureValid").Store(&v); err != nil || v.Value() != false {
		t.Fatal(v, err)
	}
	if err = obj.Call("org.freedesktop.DBus.Properties.Set", 0, BusInterface, "Connected", dbus.MakeVariant(true)).Err; err == nil {
		t.Fatal("read-only property writable")
	}
	if err = obj.Call(BusInterface+".SetFace", 0, "x", uint32(0)).Err; err == nil {
		t.Fatal("invalid ttl accepted")
	}
	if err = obj.Call(BusInterface+".ClearOverride", 0).Err; err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatal("RPC error hidden", err)
	}
	if err = obj.Call(BusInterface+".Reconnect", 0).Err; err != nil {
		t.Fatal("Reconnect not available", err)
	}
	if err = client.AddMatchSignal(dbus.WithMatchObjectPath(BusPath), dbus.WithMatchInterface(BusInterface)); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 10)
	client.Signal(signals)
	defer client.RemoveSignal(signals)
	now := time.Now()
	r.apply(func(e *Engine) []Event { return e.Begin("usb", 1, now) })
	r.apply(func(e *Engine) []Event { return e.Frame("usb", 1, snap("a"), now) })
	select {
	case sig := <-signals:
		if sig.Name != BusInterface+".Event" || !strings.Contains(sig.Body[0].(string), "connected") {
			t.Fatal(sig)
		}
	case <-time.After(time.Second):
		t.Fatal("missing Event signal")
	}
}
