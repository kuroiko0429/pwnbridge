package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"io"
	"strings"
	"testing"
	"time"
)

func testCollector() Collector {
	return Collector{ReadFile: func(string) ([]byte, error) { return nil, errors.New("absent") }, Hostname: func() (string, error) { return "test", nil }, Query: func(context.Context, string) (string, error) { return "", errors.New("I2C disconnected") }, Now: time.Now}
}

type closedWriter struct{}

func TestRPCResponseFrameBound(t *testing.T) {
	var out bytes.Buffer
	err := writeRPCResponse(context.Background(), &out, protocol.Response{OK: false, Error: strings.Repeat("<", maxFrame/2)})
	if err == nil || out.Len() > maxFrame {
		t.Fatalf("unbounded response: bytes=%d err=%v", out.Len(), err)
	}
}

func TestRPCOutputCancellationAndBound(t *testing.T) {
	for _, parentDeadline := range []time.Duration{20 * time.Millisecond, 3 * time.Second} {
		t.Run(parentDeadline.String(), func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			ctx, cancel := context.WithTimeout(context.Background(), parentDeadline)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, []string{"rpc"}, strings.NewReader("{}"), w, testCollector()) }()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("expected output error")
				}
			case <-time.After(1500 * time.Millisecond):
				t.Fatal("RPC output blocked beyond bounded deadline")
			}
		})
	}
}
func TestRPCStdinTimeoutStillResponds(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	var out bytes.Buffer
	e := Run(context.Background(), []string{"rpc"}, r, &out, testCollector())
	if !errors.Is(e, context.DeadlineExceeded) || !strings.Contains(out.String(), "deadline exceeded") {
		t.Fatalf("%v %s", e, out.String())
	}
}

func TestStreamCancellationReleasesBlockedOutput(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, []string{"stream"}, strings.NewReader(""), writer, testCollector()) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("ungraceful cancellation: %v", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("blocked stdout ignored cancellation")
	}
}

func TestRPCStdinCancellationReleasesReader(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if e := Run(ctx, []string{"rpc"}, r, &out, testCollector()); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("expected deadline: %v", e)
	}
	done := make(chan error, 1)
	go func() { _, e := w.Write([]byte("x")); done <- e }()
	select {
	case e := <-done:
		if !errors.Is(e, io.ErrClosedPipe) {
			t.Fatalf("reader not closed: %v", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RPC left a blocked stdin reader")
	}
	// Parent cancellation need not deliver a response; internal stdin timeout
	// with a live parent is covered separately.
}

func (closedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestCommandsValidFramesAndShutdown(t *testing.T) {
	c := testCollector()
	var out bytes.Buffer
	if e := Run(context.Background(), []string{"status"}, strings.NewReader(""), &out, c); e != nil {
		t.Fatal(e)
	}
	var s protocol.Snapshot
	if e := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &s); e != nil || s.SchemaVersion != 1 || s.Battery.Percent != nil || s.CPUTempC != nil {
		t.Fatalf("%s %v", out.String(), e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	out.Reset()
	if e := Run(ctx, []string{"stream", "--interval", "10ms"}, strings.NewReader(""), &out, c); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("frames: %q", out.String())
	}
	for _, line := range lines {
		if json.Unmarshal([]byte(line), &s) != nil || len(line) > maxFrame {
			t.Fatalf("bad frame %s", line)
		}
	}
	if e := Run(context.Background(), []string{"stream"}, strings.NewReader(""), closedWriter{}, c); !errors.Is(e, io.ErrClosedPipe) {
		t.Fatalf("closed stdout %v", e)
	}
	for _, args := range [][]string{{}, {"oops"}, {"stream", "--interval", "0"}, {"stream", "--socket", "/tmp/evil"}, {"status", "extra"}} {
		if Run(context.Background(), args, strings.NewReader(""), io.Discard, c) == nil {
			t.Errorf("accepted %v", args)
		}
	}
	out.Reset()
	if Run(context.Background(), []string{"rpc"}, strings.NewReader(`{"op":"shutdown"}`), &out, c) == nil {
		t.Fatal("bad RPC accepted")
	}
	var r protocol.Response
	if json.Unmarshal(out.Bytes(), &r) != nil || r.OK || r.Error == "" {
		t.Fatalf("bad RPC response %s", out.String())
	}
}
