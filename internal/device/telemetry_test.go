package device

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUIFreshnessUsesPostReadTimeAndRejectsFuture(t *testing.T) {
	base := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		delay, offset time.Duration
		available     bool
	}{
		{"future one nanosecond", 0, time.Nanosecond, false},
		{"future ten seconds", 0, 10 * time.Second, false},
		{"written during read", 20 * time.Second, 20 * time.Second, true},
		{"became stale during read", 20 * time.Second, 0, false},
		{"exact stale boundary", 15 * time.Second, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := base
			c := testCollector()
			c.Now = func() time.Time { return now }
			c.ReadFile = func(path string) ([]byte, error) {
				if path != uiPath {
					return nil, errors.New("missing")
				}
				now = base.Add(tc.delay)
				return json.Marshal(map[string]string{"face": "x", "message": "m", "mode": "M", "updated_at": base.Add(tc.offset).Format(time.RFC3339Nano)})
			}
			s := c.Snapshot(context.Background())
			if s.UIAvailable != tc.available || !s.UIAvailable && s.UIError == "" {
				t.Fatalf("%+v", s)
			}
		})
	}
}

func TestUISchemaRejectsMalformedStates(t *testing.T) {
	const stamp = `"updated_at":"2026-10-24T00:00:00Z"`
	for _, raw := range []string{
		`null`, `{` + stamp + `}`, `{"face":"x","message":null,"mode":"MANU",` + stamp + `}`,
		`{"face":"x","mode":"MANU",` + stamp + `}`, `{"face":"x","message":"m",` + stamp + `}`,
		`{"face":"x","message":"m","mode":"M","mode":"N",` + stamp + `}`,
		`{"face":"x","message":"m","mode":"M","extra":true,` + stamp + `}`,
		`{"Face":"x","message":"m","mode":"M",` + stamp + `}`,
		`{"face":[],"message":"m","mode":"M",` + stamp + `}`,
		`{"face":"x","message":"m","mode":"M","error":null,` + stamp + `}`,
	} {
		t.Run(raw, func(t *testing.T) {
			c := testCollector()
			c.Now = func() time.Time { return time.Date(2026, 10, 24, 0, 0, 1, 0, time.UTC) }
			c.ReadFile = func(path string) ([]byte, error) {
				if path == uiPath {
					return []byte(raw), nil
				}
				return nil, errors.New("absent")
			}
			s := c.Snapshot(context.Background())
			if s.UIAvailable || s.UIError == "" {
				t.Fatalf("malformed UI marked available: %+v", s)
			}
		})
	}
}

func TestSnapshotPartialFailure(t *testing.T) {
	c := Collector{ReadFile: func(path string) ([]byte, error) {
		switch path {
		case cpuPath:
			return []byte("42875\n"), nil
		case uptimePath:
			return []byte("123.5 0"), nil
		case bootPath:
			return []byte("boot-test\n"), nil
		case uiPath:
			return []byte(`{"face":"(^_^)","message":"hello","mode":"MANU","updated_at":"2026-10-24T00:00:00Z"}`), nil
		}
		return nil, errors.New("missing")
	}, Hostname: func() (string, error) { return "zero", nil }, Query: func(context.Context, string) (string, error) { return "", errors.New("I2C not connected") }, Now: func() time.Time { return time.Date(2026, 10, 24, 0, 0, 1, 0, time.UTC) }}
	s := c.Snapshot(context.Background())
	if s.SchemaVersion != 1 || s.Hostname != "zero" || s.BootID != "boot-test" || s.UptimeSeconds != 123.5 || !s.UIAvailable || s.Face != "(^_^)" || s.CPUTempC == nil || *s.CPUTempC != 42.875 {
		t.Fatalf("snapshot: %+v", s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"percent":null`) || !strings.Contains(string(b), `"voltage":null`) || s.Battery.Available || !strings.Contains(s.Battery.Error, "I2C") {
		t.Fatalf("battery: %s", b)
	}
	c.ReadFile = func(string) ([]byte, error) { return nil, errors.New("unavailable") }
	s = c.Snapshot(context.Background())
	if s.UIAvailable || s.CPUTempC != nil || s.CPUError == "" || s.UIError == "" {
		t.Fatalf("missing sensors: %+v", s)
	}
}
