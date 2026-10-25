package host

import (
	"encoding/json"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"math"
	"strings"
	"testing"
)

func TestAcceptPartialBatteryReadings(t *testing.T) {
	s := snap("a")
	v := 3.9
	charging := false
	s.Battery = protocol.Battery{Available: false, Percent: nil, Voltage: &v, Charging: &charging, Error: "battery: I2C not connected"}
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal("known partial readings must not discard all telemetry", err)
	}
	p := Properties(State{Connected: true, Snapshot: &s})
	if p["BatteryAvailable"].Value() != false {
		t.Fatal("unknown percent marked available")
	}
}
func TestSnapshotValidationAndUnknownSensors(t *testing.T) {
	good := snap("a")
	good.CPUError = "not readable"
	good.Battery = protocol.Battery{Model: "PiSugar 3", Error: "I2C not connected"}
	if err := ValidateSnapshot(good); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cpu_temp_c":null`, `"percent":null`, `"voltage":null`, `"charging":null`, `"external_power":null`} {
		if !strings.Contains(string(raw), want) {
			t.Fatal(want, string(raw))
		}
	}
	props := Properties(State{Connected: true, Snapshot: &good})
	if props["CPUTemperatureValid"].Value() != false || props["BatteryAvailable"].Value() != false {
		t.Fatal(props)
	}
	badFloat := 101.0
	cpu := 200.0
	cases := []func(*protocol.Snapshot){func(s *protocol.Snapshot) { s.SchemaVersion = 2 }, func(s *protocol.Snapshot) { s.BootID = "" }, func(s *protocol.Snapshot) { s.UptimeSeconds = math.NaN() }, func(s *protocol.Snapshot) { s.CPUTempC = &cpu }, func(s *protocol.Snapshot) { s.Battery.Available = true; s.Battery.Percent = &badFloat }, func(s *protocol.Snapshot) { v := 21.0; s.Battery.Voltage = &v }}
	for i, change := range cases {
		s := good
		change(&s)
		if ValidateSnapshot(s) == nil {
			t.Fatal("invalid snapshot accepted", i)
		}
	}
	cpu = 48.0
	good.CPUTempC = &cpu
	props = Properties(State{Connected: false, Stale: true, Snapshot: &good})
	if props["CPUTemperatureValid"].Value() != false {
		t.Fatal("stale CPU marked valid")
	}
}
