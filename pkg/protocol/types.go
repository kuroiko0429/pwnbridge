// Package protocol defines the versioned, read-only telemetry and bounded UI RPC wire formats.
package protocol

import "time"

const SchemaVersion = 1

type Battery struct {
	Available     bool     `json:"available"`
	Model         string   `json:"model"`
	Percent       *float64 `json:"percent"`
	Voltage       *float64 `json:"voltage"`
	Charging      *bool    `json:"charging"`
	ExternalPower *bool    `json:"external_power"`
	Error         string   `json:"error,omitempty"`
}

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	SampledAt     time.Time `json:"sampled_at"`
	Hostname      string    `json:"hostname"`
	BootID        string    `json:"boot_id"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Face          string    `json:"face"`
	Message       string    `json:"message"`
	Mode          string    `json:"mode"`
	UIAvailable   bool      `json:"ui_available"`
	UIUpdatedAt   time.Time `json:"ui_updated_at"`
	UIError       string    `json:"ui_error,omitempty"`
	CPUTempC      *float64  `json:"cpu_temp_c"`
	CPUError      string    `json:"cpu_error,omitempty"`
	Battery       Battery   `json:"battery"`
}

type UIState struct {
	Face      string    `json:"face"`
	Message   string    `json:"message"`
	Mode      string    `json:"mode"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

type Request struct {
	Op         string `json:"op"`
	Face       string `json:"face,omitempty"`
	Message    string `json:"message,omitempty"`
	TTLSeconds uint32 `json:"ttl_seconds,omitempty"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
