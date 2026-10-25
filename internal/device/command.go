package device

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
)

// writeRPCResponse bounds output independently of request input. Cancellation
// returns even for an inherited blocking stdout: os.File.Close itself can wait
// on a blocked Write, so cleanup must not block the caller. The executable exits
// on this error. Custom Writers/Closers that never return can retain goroutines.
func writeRPCResponse(parent context.Context, out io.Writer, response protocol.Response) error {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(b)+1 > maxFrame {
		return errors.New("RPC response frame too large")
	}
	b = append(b, '\n')
	done := make(chan error, 1)
	go func() {
		n, err := out.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if closer, ok := out.(io.Closer); ok {
			go func() { _ = closer.Close() }()
		}
		return ctx.Err()
	}
}

// Run never accepts alternate runtime paths or controller commands.
func Run(ctx context.Context, args []string, in io.Reader, out io.Writer, c Collector) error {
	if len(args) == 0 {
		return errors.New("usage: pwnbridge-agent status|stream [--interval 1s]|rpc")
	}
	switch args[0] {
	case "status", "stream":
		stopOutput := context.AfterFunc(ctx, func() {
			if closer, ok := out.(io.Closer); ok {
				_ = closer.Close()
			}
		})
		defer stopOutput()
		interval := time.Second
		if args[0] == "status" {
			if len(args) != 1 {
				return errors.New("status takes no arguments")
			}
		} else {
			f := flag.NewFlagSet("stream", flag.ContinueOnError)
			f.SetOutput(io.Discard)
			f.DurationVar(&interval, "interval", time.Second, "sampling interval")
			if e := f.Parse(args[1:]); e != nil {
				return e
			}
			if f.NArg() != 0 || interval < 10*time.Millisecond || interval > time.Minute {
				return errors.New("interval must be between 10ms and 1m")
			}
		}
		emit := func() error {
			b, e := json.Marshal(c.Snapshot(ctx))
			if e != nil {
				return e
			}
			if len(b)+1 > maxFrame {
				return errors.New("snapshot frame too large")
			}
			b = append(b, '\n')
			n, e := out.Write(b)
			if e == nil && n != len(b) {
				e = io.ErrShortWrite
			}
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if ctx.Err() != nil {
			return nil
		}
		if e := emit(); e != nil {
			return e
		}
		if args[0] == "status" {
			return nil
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if e := emit(); e != nil {
					return e
				}
			}
		}
	case "rpc":
		if len(args) != 1 {
			return errors.New("rpc takes no arguments")
		}
		type result struct {
			b []byte
			e error
		}
		ch := make(chan result, 1)
		go func() { b, e := readBounded(in, maxFrame); ch <- result{b, e} }()
		deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var b []byte
		var e error
		select {
		case <-deadline.Done():
			e = deadline.Err()
			// The executable owns stdin. Closing it releases the bounded reader
			// goroutine as well as returning a deadline response to the caller.
			if closer, ok := in.(io.Closer); ok {
				_ = closer.Close()
			}
		case v := <-ch:
			b, e = v.b, v.e
		}
		var resp protocol.Response
		if e == nil {
			var r protocol.Request
			r, e = DecodeRequest(b)
			if e == nil {
				resp, e = proxyRPC(deadline, ControlSocket, r, time.Second)
			}
		}
		if e != nil {
			resp = protocol.Response{OK: false, Error: e.Error()}
		}
		// Use the parent, not the expired stdin/RPC deadline: a live caller
		// must still receive the stdin-timeout response. Output has its own bound.
		if writeErr := writeRPCResponse(ctx, out, resp); writeErr != nil {
			return writeErr
		}
		if e != nil {
			return e
		}
		if !resp.OK {
			return errors.New(resp.Error)
		}
		return nil
	default:
		return errors.New("unknown command")
	}
}
