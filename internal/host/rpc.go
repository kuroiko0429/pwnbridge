package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"io"
	"os/exec"
	"strings"
)

type boundedOutput struct {
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxFrameBytes - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func RunRPC(ctx context.Context, cmd *exec.Cmd, r protocol.Request) error {
	if err := ValidateRequest(r); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(append(data, '\n'))
	out := &boundedOutput{}
	stderr := &cappedBuffer{}
	cmd.Stdout = out
	cmd.Stderr = stderr
	prepareProcess(cmd)
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SSH RPC: %w: %s", err, strings.TrimSpace(string(stderr.data)))
	}
	if out.overflow {
		return errors.New("RPC response exceeds size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(out.data))
	var resp protocol.Response
	if err = dec.Decode(&resp); err != nil {
		return fmt.Errorf("invalid RPC response: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return errors.New("RPC response must be one JSON object")
	}
	if resp.Error != "" {
		return fmt.Errorf("remote RPC: %s", resp.Error)
	}
	if !resp.OK {
		if resp.Error == "" {
			return errors.New("remote RPC failed without diagnostic")
		}
		return fmt.Errorf("remote RPC: %s", resp.Error)
	}
	return nil
}
