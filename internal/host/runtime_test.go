package host

import (
	"context"
	"errors"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeLiveWorkersFallbackRecovery(t *testing.T) {
	c := goodConfig()
	c.Endpoints = append(c.Endpoints, Endpoint{Name: "bluetooth", Address: "10.77.0.2"})
	c.PollInterval.Duration = 100 * time.Millisecond
	c.IdleTimeout.Duration = 300 * time.Millisecond
	c.StaleAfter.Duration = 600 * time.Millisecond
	c.RetryInterval.Duration = 100 * time.Millisecond
	r := NewRuntime(c)
	var btDown, usbDown atomic.Bool
	r.gate = func(e Endpoint) (Binding, error) {
		if (e.Name == "bluetooth" && btDown.Load()) || (e.Name == "usb" && usbDown.Load()) {
			return Binding{}, errors.New("interface down")
		}
		return Binding{Interface: e.Name, Source: "10.12.194.2"}, nil
	}
	r.command = func(ctx context.Context, e Endpoint, b Binding, op string) *exec.Cmd {
		return helperCmd(ctx, "continuous")
	}
	events := make(chan Event, 30)
	r.notify = func(s State, ev []Event) {
		for _, v := range ev {
			events <- v
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	wait := func(transport string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			s := r.Snapshot()
			if s.Transport == transport && s.Connected == (transport != "") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("selection timeout", s)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait("bluetooth")
	btDown.Store(true)
	wait("usb")
	btDown.Store(false)
	wait("bluetooth")
	for len(events) > 0 {
		if v := <-events; v.Type == "disconnected" {
			t.Fatal("standby was not used", v)
		}
	}
	btDown.Store(true)
	usbDown.Store(true)
	wait("")
	if !r.Snapshot().Stale {
		t.Fatal("both lost not stale")
	}
	usbDown.Store(false)
	wait("usb")
	recovered := false
	for len(events) > 0 {
		v := <-events
		recovered = recovered || v.Type == "recovered"
	}
	if !recovered {
		t.Fatal("missing recovered event")
	}
}
func TestRuntimeReconnectStartsNewGenerationImmediately(t *testing.T) {
	c := goodConfig()
	c.RetryInterval.Duration = 10 * time.Second
	r := NewRuntime(c)
	r.gate = func(Endpoint) (Binding, error) { return Binding{Interface: "test", Source: "10.12.194.2"}, nil }
	r.command = func(ctx context.Context, e Endpoint, b Binding, op string) *exec.Cmd { return helperCmd(ctx, "stream") }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor := func(g uint64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			s := r.Snapshot()
			if s.Connected && s.Transports["usb"].Generation >= g {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("generation not restarted", s)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(1)
	r.Reconnect()
	waitFor(2)
}
func TestRuntimeRunsStandbyStreamsAndRPC(t *testing.T) {
	c := goodConfig()
	c.Endpoints = append(c.Endpoints, Endpoint{Name: "bluetooth", Address: "10.77.0.2"})
	c.BluetoothHealthyFrames = 1
	r := NewRuntime(c)
	r.gate = func(e Endpoint) (Binding, error) { return Binding{Interface: "test", Source: "10.12.194.2"}, nil }
	r.command = func(ctx context.Context, e Endpoint, b Binding, op string) *exec.Cmd {
		if op == "rpc" {
			return rpcHelper(ctx)
		}
		return helperCmd(ctx, "stream")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := r.Snapshot()
		if s.Transport == "bluetooth" && s.Transports["usb"].Fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.RPC(context.Background(), protocol.Request{Op: "set_face", Face: "fail", TTLSeconds: 30}); err == nil {
		t.Fatal("remote error hidden")
	}
	if r.Snapshot().LastError == "" {
		t.Fatal("RPC diagnostic missing")
	}
	if err := r.RPC(context.Background(), protocol.Request{Op: "clear_override"}); err != nil {
		t.Fatal(err)
	}
}
