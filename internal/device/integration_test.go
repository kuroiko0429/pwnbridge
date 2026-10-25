package device

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
)

// Exercise the real Python Unix server against the real Go wire client and
// collector. Only the missing Pwnagotchi SDK and filesystem ownership are stubbed.
func TestPythonBridge(t *testing.T) {
	python := os.Getenv("PWNBRIDGE_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	if _, e := exec.LookPath(python); e != nil {
		t.Skip("Python not installed")
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "../../device/tests/bridge_fixture.py", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	line, e := bufio.NewReader(stdout).ReadString('\n')
	if e != nil {
		t.Fatalf("fixture startup: %v %s", e, stderr.String())
	}
	if line != "{\"ready\": true}\n" {
		t.Fatalf("fixture: %s", line)
	}
	c := testCollector()
	c.ReadFile = func(path string) ([]byte, error) {
		if path == uiPath {
			return readBoundedFile(filepath.Join(dir, "ui.json"))
		}
		return readBoundedFile(path)
	}
	await := func(face, message string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s := c.Snapshot(ctx)
			if s.UIAvailable && s.Face == face && s.Message == message {
				b, e := json.Marshal(s)
				if e != nil || len(b) > maxFrame || s.Battery.Percent != nil {
					t.Fatalf("frame: %s %v", b, e)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing UI state: %+v", c.Snapshot(ctx))
	}
	await("native", "hello")
	call := func(r protocol.Request) {
		t.Helper()
		response, e := proxyRPC(ctx, filepath.Join(dir, "control.sock"), r, time.Second)
		if e != nil || !response.OK {
			t.Fatalf("RPC: %+v %v", response, e)
		}
	}
	call(protocol.Request{Op: "set_face", Face: "(◕‿◕)", TTLSeconds: 1})
	await("(◕‿◕)", "hello")
	await("native", "hello") // Actual monotonic TTL expiry on the background worker.
	call(protocol.Request{Op: "set_message", Message: "日本語メッセージ", TTLSeconds: 10})
	await("native", "日本語メッセージ")
	call(protocol.Request{Op: "clear_override"})
	await("native", "hello")
	cmd.Process.Signal(syscall.SIGTERM)
	e = cmd.Wait()
	waited = true
	if e != nil {
		t.Fatalf("fixture shutdown: %v %s", e, stderr.String())
	}
	for _, name := range []string{"ui.json", "control.sock"} {
		if _, e := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(e) {
			t.Fatalf("leaked %s: %v", name, e)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("fixture stderr: %s", stderr.String())
	}
}
