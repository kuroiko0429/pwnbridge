package host

import (
	"errors"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"math"
)

func ValidateSnapshot(s protocol.Snapshot) error {
	if s.SchemaVersion != protocol.SchemaVersion {
		return fmt.Errorf("unsupported telemetry schema_version %d", s.SchemaVersion)
	}
	if s.BootID == "" || s.Hostname == "" || s.SampledAt.IsZero() {
		return errors.New("telemetry missing boot_id, hostname or sampled_at")
	}
	if math.IsNaN(s.UptimeSeconds) || math.IsInf(s.UptimeSeconds, 0) || s.UptimeSeconds < 0 {
		return errors.New("invalid uptime_seconds")
	}
	if s.CPUTempC != nil && !inRange(*s.CPUTempC, -50, 150) {
		return errors.New("invalid cpu_temp_c")
	}
	b := s.Battery
	if b.Percent != nil && !inRange(*b.Percent, 0, 100) {
		return errors.New("invalid battery percent")
	}
	if b.Voltage != nil && !inRange(*b.Voltage, 0, 20) {
		return errors.New("invalid battery voltage")
	}
	// Individual PiSugar queries can fail independently; preserve known partial readings.
	return nil
}
func inRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi
}
