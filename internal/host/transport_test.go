package host

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestStreamHelperProcess(t *testing.T) {
	if os.Getenv("PWNBRIDGE_HELPER") != "1" {
		return
	}
	mode := os.Getenv("PWNBRIDGE_HELPER_MODE")
	if mode == "bad" {
		fmt.Println(`{"schema_version":99}`)
		os.Exit(0)
	}
	if mode == "large" {
		fmt.Println(strings.Repeat("x", MaxFrameBytes+1))
		os.Exit(0)
	}
	if mode != "idle" {
		json.NewEncoder(os.Stdout).Encode(snap("helper"))
	}
	if mode == "eof" {
		os.Exit(0)
	}
	if mode == "continuous" {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if err := json.NewEncoder(os.Stdout).Encode(snap("helper")); err != nil {
				os.Exit(0)
			}
		}
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
func helperCmd(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStreamHelperProcess$")
	cmd.Env = append(os.Environ(), "PWNBRIDGE_HELPER=1", "PWNBRIDGE_HELPER_MODE="+mode, "GORACE=atexit_sleep_ms=0")
	return cmd
}
func TestStreamingHelperIdleAndReap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := helperCmd(ctx, "stream")
	frames := 0
	err := RunStream(ctx, cmd, 100*time.Millisecond, func(s protocol.Snapshot) {
		frames++
		if s.BootID != "helper" {
			t.Error(s)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "idle timeout") || frames != 1 {
		t.Fatal(err, frames)
	}
	if cmd.ProcessState == nil {
		t.Fatal("not reaped")
	}
}

func TestStreamingRejectsBadFramesEOFAndCancellation(t *testing.T) {
	for _, mode := range []string{"bad", "large", "eof", "idle"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cmd := helperCmd(ctx, mode)
			n := 0
			err := RunStream(ctx, cmd, 100*time.Millisecond, func(protocol.Snapshot) { n++ })
			if err == nil {
				t.Fatal("missing stream error")
			}
			if mode != "eof" && n != 0 {
				t.Fatalf("invalid frames accepted: %d (%v)", n, err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("not reaped")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := helperCmd(ctx, "stream")
	err := RunStream(ctx, cmd, time.Second, func(protocol.Snapshot) { cancel() })
	if err != context.Canceled {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("cancel did not reap")
	}
}
func TestGateAndSSHIdentity(t *testing.T) {
	e := Endpoint{Name: "usb", Address: "10.12.194.1", Interface: "enp0s20f0u*"}
	rows := []LocalInterface{{Name: "wlan0", Up: true, Addresses: []string{"192.168.0.2/24"}}, {Name: "enp0s20f0u2", Up: false, Addresses: []string{"10.12.194.2/24"}}}
	if _, err := MatchInterface(e, rows); err == nil {
		t.Fatal("down interface allowed")
	}
	rows[1].Up = true
	b, err := MatchInterface(e, rows)
	if err != nil {
		t.Fatal(err)
	}
	if net.ParseIP(b.Source) == nil {
		t.Fatal(b)
	}
	args := SSHArgs(goodConfig(), e, b, "stream")
	s := strings.Join(args, " ")
	for _, part := range []string{"StrictHostKeyChecking=yes", "UpdateHostKeys=no", "CheckHostIP=no", "HostKeyAlias=10.12.194.1", "BatchMode=yes", "IdentitiesOnly=yes", "-b 10.12.194.2", "/usr/local/bin/pwnbridge-agent stream --interval 1s"} {
		if !strings.Contains(s, part) {
			t.Errorf("missing %s: %s", part, s)
		}
	}
	e.Interface = "pwnpan0"
	if _, err = MatchInterface(e, rows); err == nil {
		t.Fatal("wrong interface allowed")
	}
	e.Interface = ""
	if _, err = MatchInterface(e, rows); err != nil {
		t.Fatal(err)
	}
}
