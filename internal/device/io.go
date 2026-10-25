package device

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

func readBounded(r io.Reader, limit int) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if e != nil {
		return nil, e
	}
	if len(b) > limit {
		return nil, errors.New("frame too large")
	}
	return b, nil
}

const piSugarSocket = "/tmp/pisugar-server.sock"

func queryPiSugar(ctx context.Context, key string) (string, error) {
	return querySocket(ctx, piSugarSocket, key, 150*time.Millisecond)
}
func querySocket(ctx context.Context, path, key string, timeout time.Duration) (string, error) {
	switch key {
	case "model", "battery", "battery_v", "battery_charging", "battery_power_plugged":
	default:
		return "", errors.New("unsupported read-only query")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return "", e
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, e = fmt.Fprintf(c, "get %s\n", key); e != nil {
		return "", e
	}
	r := bufio.NewReader(io.LimitReader(c, 1025))
	line, e := r.ReadString('\n')
	if e != nil {
		return "", e
	}
	if len(line) > 1024 {
		return "", errors.New("PiSugar response too large")
	}
	line = strings.TrimSpace(line)
	prefix := key + ": "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("PiSugar: %s", line)
	}
	return strings.TrimPrefix(line, prefix), nil
}
