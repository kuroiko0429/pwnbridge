// Package device implements read-only telemetry and the bounded local UI bridge.
package device

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
)

const (
	cpuPath    = "/sys/class/thermal/thermal_zone0/temp"
	uptimePath = "/proc/uptime"
	bootPath   = "/proc/sys/kernel/random/boot_id"
	uiPath     = "/run/pwnbridge/ui.json"
	maxFrame   = 16384
)

// Collector dependencies are injectable for local tests; the executable uses fixed paths.
type Collector struct {
	ReadFile func(string) ([]byte, error)
	Hostname func() (string, error)
	Query    func(context.Context, string) (string, error)
	Now      func() time.Time
}

func NewCollector() Collector {
	return Collector{ReadFile: readBoundedFile, Hostname: os.Hostname, Query: queryPiSugar, Now: time.Now}
}

func readBoundedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(f, maxFrame)
}

func decodeUIState(b []byte, ui *protocol.UIState) error {
	fields, e := strictObject(b)
	if e != nil {
		return e
	}
	for _, key := range []string{"face", "message", "mode", "updated_at"} {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing UI field %s", key)
		}
	}
	for key, raw := range fields {
		switch key {
		case "face", "message", "mode", "updated_at", "error":
		default:
			return fmt.Errorf("unknown UI field %s", key)
		}
		var text string
		if e := json.Unmarshal(raw, &text); e != nil {
			return e
		}
	}
	return json.Unmarshal(b, ui)
}

func (c Collector) Snapshot(ctx context.Context) protocol.Snapshot {
	s := protocol.Snapshot{SchemaVersion: protocol.SchemaVersion, SampledAt: c.Now().UTC()}
	s.Hostname, _ = c.Hostname()
	if b, e := c.ReadFile(bootPath); e == nil {
		s.BootID = strings.TrimSpace(string(b))
	}
	if b, e := c.ReadFile(uptimePath); e == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			v, e := strconv.ParseFloat(fields[0], 64)
			if e == nil && finiteRange(v, 0, 1e12) {
				s.UptimeSeconds = v
			}
		}
	}
	if b, e := c.ReadFile(cpuPath); e != nil {
		s.CPUError = e.Error()
	} else {
		v, e := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if e != nil || !finiteRange(v, -40000, 150000) {
			s.CPUError = "invalid CPU temperature"
		} else {
			v /= 1000
			s.CPUTempC = &v
		}
	}
	var ui protocol.UIState
	if b, e := c.ReadFile(uiPath); e != nil {
		s.UIError = e.Error()
	} else if len(b) > maxFrame {
		s.UIError = "UI frame too large"
	} else if e = decodeUIState(b, &ui); e != nil {
		s.UIError = "invalid UI JSON"
	} else if ui.UpdatedAt.IsZero() || len(ui.Face) > 1024 || len(ui.Message) > 8192 || len(ui.Mode) > 128 {
		s.UIError = "invalid UI state"
	} else if ui.Error != "" {
		s.UIError = ui.Error
	} else if age := c.Now().Sub(ui.UpdatedAt); age > 15*time.Second || age < 0 {
		s.UIError = "stale UI state"
	} else {
		s.UIAvailable = true
		s.Face = ui.Face
		s.Message = ui.Message
		s.Mode = ui.Mode
		s.UIUpdatedAt = ui.UpdatedAt
	}
	s.Battery = c.battery(ctx)
	return s
}

func finiteRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi
}

func (c Collector) battery(ctx context.Context) protocol.Battery {
	b := protocol.Battery{}
	errs := []string{}
	query := func(key string) (string, bool) {
		v, e := c.Query(ctx, key)
		if e != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, e))
			return "", false
		}
		return v, true
	}
	if v, ok := query("model"); ok {
		b.Model = v
	}
	number := func(key string, lo, hi float64) *float64 {
		raw, ok := query(key)
		if !ok {
			return nil
		}
		v, e := strconv.ParseFloat(raw, 64)
		if e != nil || !finiteRange(v, lo, hi) {
			errs = append(errs, key+": invalid reading")
			return nil
		}
		return &v
	}
	boolean := func(key string) *bool {
		raw, ok := query(key)
		if !ok {
			return nil
		}
		if raw != "true" && raw != "false" {
			errs = append(errs, key+": invalid reading")
			return nil
		}
		v := raw == "true"
		return &v
	}
	b.Percent = number("battery", 0, 100)
	b.Voltage = number("battery_v", 2, 5.5)
	b.Charging = boolean("battery_charging")
	b.ExternalPower = boolean("battery_power_plugged")
	b.Available = b.Percent != nil
	b.Error = strings.Join(errs, "; ")
	return b
}
