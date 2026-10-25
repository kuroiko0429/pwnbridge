package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const MaxFrameBytes = 64 * 1024
const maxDiagnosticBytes = 4096

// cappedBuffer discards excess diagnostic bytes without blocking the child.
type cappedBuffer struct{ data []byte }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxDiagnosticBytes - len(b.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		b.data = append(b.data, p[:remaining]...)
	}
	return n, nil
}
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// RunStream owns and reaps cmd. Only bounded, decoded frames reset the idle timer.
func RunStream(ctx context.Context, cmd *exec.Cmd, idle time.Duration, onFrame func(protocol.Snapshot)) (result error) {
	prepareProcess(cmd)
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		out.Close()
		return fmt.Errorf("start stream: %w", err)
	}
	frames := make(chan []byte)
	scanDone := make(chan error, 1)
	scanCtx, stop := context.WithCancel(ctx)
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), MaxFrameBytes)
		for scanner.Scan() {
			b := append([]byte(nil), scanner.Bytes()...)
			select {
			case frames <- b:
			case <-scanCtx.Done():
				scanDone <- scanCtx.Err()
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		scanDone <- err
	}()
	defer func() {
		stop()
		killProcess(cmd)
		out.Close()
		waitErr := cmd.Wait()
		if result == nil {
			result = waitErr
		}
		if len(stderr.data) > 0 && result != nil {
			result = fmt.Errorf("%w: %s", result, strings.TrimSpace(string(stderr.data)))
		}
	}()
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("stream idle timeout after %s", idle)
		case err := <-scanDone:
			return fmt.Errorf("stream: %w", err)
		case b := <-frames:
			var s protocol.Snapshot
			if err := json.Unmarshal(b, &s); err != nil {
				return fmt.Errorf("malformed telemetry JSON: %w", err)
			}
			if err := ValidateSnapshot(s); err != nil {
				return fmt.Errorf("invalid telemetry: %w", err)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
			onFrame(s)
		}
	}
}
