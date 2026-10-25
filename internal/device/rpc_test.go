package device

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestCrossLanguageParity(t *testing.T) {
	b, e := os.ReadFile("../../device/tests/request_cases.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Raw   string
		Valid bool
	}
	var rows [][]json.RawMessage
	if e = json.Unmarshal(b, &rows); e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		var c struct {
			Raw   string
			Valid bool
		}
		json.Unmarshal(row[0], &c.Raw)
		json.Unmarshal(row[1], &c.Valid)
		cases = append(cases, c)
	}
	for _, c := range cases {
		if _, e := DecodeRequest([]byte(c.Raw)); (e == nil) != c.Valid {
			t.Errorf("Go accepted=%v want=%v %s", e == nil, c.Valid, c.Raw)
		}
	}
	python := os.Getenv("PWNBRIDGE_TEST_PYTHON")
	if python == "" {
		python = "python3"
	}
	cmd := exec.Command(python, "-c", `import sys, json
sys.path.insert(0, '../../device/tests')
from test_pwnbridge import bridge
for raw, valid in json.load(sys.stdin):
 try:
  bridge.decode_request(raw.encode('utf-8'))
  accepted = True
 except ValueError:
  accepted = False
 assert accepted == valid, (raw, accepted, valid)
`)
	cmd.Stdin = strings.NewReader(string(b))
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("Python parity: %v %s", e, out)
	}
}

func TestRPCValidationAndSocket(t *testing.T) {
	valid := `{"op":"set_face","face":"(◕‿◕)","ttl_seconds":30}`
	r, e := DecodeRequest([]byte(valid))
	if e != nil || r.Face != "(◕‿◕)" {
		t.Fatalf("%+v %v", r, e)
	}
	for _, bad := range []string{`{}`, `{"op":"shutdown"}`, `{"op":"clear_override","path":"/etc/passwd"}`, `{"op":"set_face","ttl_seconds":0}`, `{"op":"set_message","ttl_seconds":301}`, `{"op":"clear_override"} {}`, `{"op":"set_face","face":"` + strings.Repeat("a", 65) + `","ttl_seconds":1}`, `{"op":"set_message","message":"` + strings.Repeat("界", 513) + `","ttl_seconds":1}`, `{"op":"clear_override","ttl_seconds":1}`, `{"op":"set_face","message":"x","ttl_seconds":1}`, `{"op":"clear_override","op":"clear_override"}`, strings.Repeat(" ", maxFrame+1)} {
		if _, e := DecodeRequest([]byte(bad)); e == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	path := filepath.Join(t.TempDir(), "s")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	go func() {
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		var req map[string]any
		json.Unmarshal([]byte(line), &req)
		if req["op"] == "set_face" {
			c.Write([]byte("{\"ok\":true}\n"))
		}
	}()
	resp, e := proxyRPC(context.Background(), path, r, time.Second)
	if e != nil || !resp.OK {
		t.Fatalf("%+v %v", resp, e)
	}
	if ControlSocket != "/run/pwnbridge/control.sock" {
		t.Fatal("unsafe production path")
	}
}

func TestRPCBadReplies(t *testing.T) {
	base := t.TempDir()
	for _, response := range []string{`{}`, `{"ok":null}`, `{"ok":true,"ok":false}`, `{"ok":false}`, `{"ok":true,"error":"contradiction"}`, `{"ok":true,"extra":1}`, `{"ok":true} {}`, strings.Repeat("x", maxFrame+1), "STALL"} {
		t.Run(response[:min(32, len(response))], func(t *testing.T) {
			path := filepath.Join(base, "s")
			l, e := net.Listen("unix", path)
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, e := l.Accept()
				if e != nil {
					return
				}
				defer c.Close()
				bufio.NewReader(c).ReadString('\n')
				if response == "STALL" {
					time.Sleep(75 * time.Millisecond)
				} else {
					c.Write([]byte(response + "\n"))
				}
			}()
			if _, e := proxyRPC(context.Background(), path, protocol.Request{Op: "clear_override"}, 20*time.Millisecond); e == nil {
				t.Errorf("accepted invalid response %q", response)
			}
			<-done
		})
	}
}
