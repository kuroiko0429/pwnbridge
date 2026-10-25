package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A genuinely full inherited blocking stdout, not merely an io.Pipe stub.
func TestRPCBinaryFullStdout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agent")
	if b, e := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	for _, signal := range []bool{true, false} {
		name := "output deadline"
		if signal {
			name = "SIGTERM"
		}
		t.Run(name, func(t *testing.T) {
			r, w, e := os.Pipe()
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			defer w.Close()
			fd := int(w.Fd())
			if e = syscall.SetNonblock(fd, true); e != nil {
				t.Fatal(e)
			}
			for {
				_, e = syscall.Write(fd, make([]byte, 4096))
				if errors.Is(e, syscall.EAGAIN) {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			if e = syscall.SetNonblock(fd, false); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "rpc")
			cmd.Stdin = strings.NewReader("{}")
			cmd.Stdout = w
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			if signal {
				time.Sleep(100 * time.Millisecond)
				if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("unexpected exit: %v; stderr=%s", err, stderr.String())
				}
				want := "context deadline exceeded"
				if signal {
					want = "context canceled"
				}
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("missing cancellation diagnostic %q: %s", want, stderr.String())
				}
			case <-time.After(1500 * time.Millisecond):
				cmd.Process.Kill()
				<-done
				t.Fatalf("blocked stdout ignored %s; stderr=%s", name, stderr.String())
			}
		})
	}
}
