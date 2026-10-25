package device

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiSugarBoundedReadOnlyProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				s, _ := bufio.NewReader(c).ReadString('\n')
				switch s {
				case "get model\n":
					c.Write([]byte("model: PiSugar 3\n"))
				case "get battery\n":
					c.Write([]byte("error: I2C not connected\n"))
				case "get battery_v\n":
					c.Write([]byte(strings.Repeat("x", 2048) + "\n"))
				case "get battery_charging\n":
					time.Sleep(150 * time.Millisecond)
				}
			}()
		}
	}()
	q := func(key string) (string, error) {
		return querySocket(context.Background(), path, key, 40*time.Millisecond)
	}
	if v, e := q("model"); e != nil || v != "PiSugar 3" {
		t.Fatalf("model %q %v", v, e)
	}
	for _, key := range []string{"battery", "battery_v", "battery_charging", "set battery 0", "../../etc/passwd"} {
		if _, e := q(key); e == nil {
			t.Errorf("accepted %q", key)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := querySocket(ctx, path, "model", time.Second); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestBatteryRangeValidation(t *testing.T) {
	c := Collector{Query: func(_ context.Context, k string) (string, error) {
		return map[string]string{"model": "PiSugar 3", "battery": "42", "battery_v": "3.9", "battery_charging": "false", "battery_power_plugged": "true"}[k], nil
	}}
	b := c.battery(context.Background())
	if !b.Available || *b.Percent != 42 || *b.Voltage != 3.9 || *b.Charging || !*b.ExternalPower || b.Error != "" {
		t.Fatalf("%+v", b)
	}
	for _, bad := range []string{"NaN", "Inf", "-1", "101", "I2C not connected"} {
		c.Query = func(_ context.Context, k string) (string, error) { return bad, nil }
		b = c.battery(context.Background())
		if b.Available || b.Percent != nil || b.Voltage != nil || b.Charging != nil || b.ExternalPower != nil || b.Error == "" {
			t.Fatalf("accepted %s: %+v", bad, b)
		}
	}
}
