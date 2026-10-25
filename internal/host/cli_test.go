package host

import (
	"bytes"
	"context"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestCLIValidatesBeforeBus(t *testing.T) {
	for _, args := range [][]string{{}, {"bad"}, {"face"}, {"face", "--ttl", "0s", "x"}, {"face", "--ttl", "1500ms", "x"}, {"message", "x", "--ttl", "30s"}, {"status", "extra"}, {"clear", "extra"}, {"reconnect", "--bad"}, {"daemon", "--bad"}} {
		var out bytes.Buffer
		if err := RunCLI(context.Background(), args, &out, &out); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestHumanStatusShowsUnknownAndStale(t *testing.T) {
	s := State{Stale: true, Snapshot: func() *protocol.Snapshot {
		v := snap("a")
		v.Face = "(^_^)"
		v.Message = "hello"
		v.Battery.Error = "I2C not connected"
		return &v
	}()}
	s.LastSeen = time.Unix(1, 0).Format(time.RFC3339)
	text := FormatStatus(s)
	for _, want := range []string{"STALE", "(^_^)", "hello", "unknown", "I2C not connected"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
}
