package host

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRequestValidation(t *testing.T) {
	for _, r := range []protocol.Request{{Op: "reboot"}, {Op: "set_face", Face: strings.Repeat("顔", 65), TTLSeconds: 1}, {Op: "set_message", Message: "x", TTLSeconds: 301}, {Op: "set_face", TTLSeconds: 0}, {Op: "clear_override", TTLSeconds: 1}} {
		if ValidateRequest(r) == nil {
			t.Fatal("accepted", r)
		}
	}
	if err := ValidateRequest(protocol.Request{Op: "set_face", Face: "(^_^)", TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}
}
func TestRPCBoundsAndContradictoryResponse(t *testing.T) {
	for _, face := range []string{"contradictory", "sleep", "large"} {
		t.Run(face, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			cmd := rpcHelper(ctx)
			err := RunRPC(ctx, cmd, protocol.Request{Op: "set_face", Face: face, TTLSeconds: 30})
			if err == nil {
				t.Fatal("invalid RPC response accepted")
			}
			if cmd.ProcessState == nil {
				t.Fatal("not reaped")
			}
		})
	}
}
func TestRPCReportsRemoteFailure(t *testing.T) {
	ctx := context.Background()
	r := protocol.Request{Op: "set_face", Face: "fail", TTLSeconds: 30}
	if err := RunRPC(ctx, rpcHelper(ctx), r); err == nil || !strings.Contains(err.Error(), "UI unavailable") {
		t.Fatal(err)
	}
	r.Face = "ok"
	if err := RunRPC(ctx, rpcHelper(ctx), r); err != nil {
		t.Fatal(err)
	}
}
func TestRPCHelperProcess(t *testing.T) {
	if os.Getenv("PWNBRIDGE_RPC_HELPER") != "1" {
		return
	}
	var r protocol.Request
	json.NewDecoder(os.Stdin).Decode(&r)
	if r.Face == "contradictory" {
		fmt.Println(`{"ok":true,"error":"not applied"}`)
		os.Exit(0)
	}
	if r.Face == "sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if r.Face == "large" {
		fmt.Println(strings.Repeat("x", MaxFrameBytes+1))
		os.Exit(0)
	}
	if r.Face == "fail" {
		fmt.Println(`{"ok":false,"error":"UI unavailable"}`)
	} else {
		fmt.Println(`{"ok":true}`)
	}
	os.Exit(0)
}
func rpcHelper(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRPCHelperProcess$")
	cmd.Env = append(os.Environ(), "PWNBRIDGE_RPC_HELPER=1", "GORACE=atexit_sleep_ms=0")
	return cmd
}
