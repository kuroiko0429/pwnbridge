package host

import (
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"time"
)

type Event struct {
	Type              string    `json:"type"`
	At                time.Time `json:"at"`
	Transport         string    `json:"transport"`
	PreviousTransport string    `json:"previous_transport,omitempty"`
	BootID            string    `json:"boot_id,omitempty"`
}
type LinkState struct {
	Generation uint64 `json:"generation"`
	Fresh      bool   `json:"fresh"`
	LastSeen   string `json:"last_seen"`
	Error      string `json:"error"`
}
type State struct {
	Connected  bool                 `json:"connected"`
	Transport  string               `json:"transport"`
	Stale      bool                 `json:"stale"`
	LastSeen   string               `json:"last_seen"`
	LastError  string               `json:"last_error"`
	Snapshot   *protocol.Snapshot   `json:"snapshot"`
	Transports map[string]LinkState `json:"transports"`
}
type link struct {
	generation uint64
	running    bool
	valid      bool
	seen       time.Time
	frames     int
	snapshot   protocol.Snapshot
	err        string
}

// Engine is deterministic: callers serialize access and supply host receipt time.
type Engine struct {
	config        Config
	links         map[string]*link
	state         State
	everConnected bool
	retiredBoots  map[string]bool
}

func NewEngine(c Config) *Engine {
	e := &Engine{config: c, retiredBoots: map[string]bool{}, links: map[string]*link{}, state: State{Stale: true, Transports: map[string]LinkState{}}}
	for _, v := range c.Endpoints {
		e.links[v.Name] = &link{}
	}
	return e
}
func (e *Engine) State() State { return e.state }
func (e *Engine) Begin(name string, g uint64, now time.Time) []Event {
	l := e.links[name]
	if l == nil {
		l = &link{}
		e.links[name] = l
	}
	if g <= l.generation {
		return nil
	}
	*l = link{generation: g, running: true}
	return e.update(now)
}
func (e *Engine) Frame(name string, g uint64, s protocol.Snapshot, now time.Time) []Event {
	l := e.links[name]
	if l == nil || g != l.generation || !l.running {
		return nil
	}
	if e.retiredBoots[s.BootID] {
		return nil
	}
	if old := e.state.Snapshot; old != nil && old.BootID != "" && old.BootID != s.BootID {
		e.retiredBoots[old.BootID] = true
		for _, other := range e.links {
			other.valid = false
			other.frames = 0
		}
	}
	if !e.fresh(l, now) {
		l.frames = 0
	}
	l.valid = true
	l.seen = now
	l.frames++
	l.snapshot = s
	l.err = ""
	return e.update(now)
}
func (e *Engine) Fail(name string, g uint64, msg string, now time.Time) []Event {
	l := e.links[name]
	if l == nil || g != l.generation {
		return nil
	}
	l.running = false
	l.valid = false
	l.frames = 0
	l.err = msg
	return e.update(now)
}
func (e *Engine) Tick(now time.Time) []Event { return e.update(now) }
func (e *Engine) fresh(l *link, now time.Time) bool {
	return l != nil && l.valid && now.Sub(l.seen) < e.config.StaleAfter.Duration
}
func (e *Engine) update(now time.Time) []Event {
	old := e.state
	chosen := ""
	bt, usb := e.links["bluetooth"], e.links["usb"]
	if e.fresh(bt, now) && (old.Transport != "usb" || bt.frames >= e.config.BluetoothHealthyFrames || !e.fresh(usb, now)) {
		chosen = "bluetooth"
	} else if e.fresh(usb, now) {
		chosen = "usb"
	}
	e.state.Connected = chosen != ""
	e.state.Stale = chosen == ""
	e.state.Transport = chosen
	e.state.Transports = map[string]LinkState{}
	for name, l := range e.links {
		seen := ""
		if !l.seen.IsZero() {
			seen = l.seen.UTC().Format(time.RFC3339Nano)
		}
		e.state.Transports[name] = LinkState{l.generation, e.fresh(l, now), seen, l.err}
	}
	if chosen != "" {
		l := e.links[chosen]
		s := l.snapshot
		e.state.Snapshot = &s
		e.state.LastSeen = l.seen.UTC().Format(time.RFC3339Nano)
	}
	e.state.LastError = ""
	if chosen == "" {
		e.state.LastError = "no fresh authenticated telemetry"
		for _, name := range []string{"bluetooth", "usb"} {
			if l := e.links[name]; l != nil && l.err != "" {
				e.state.LastError += "; " + name + ": " + l.err
			}
		}
	}
	var events []Event
	if old.Connected && chosen == "" {
		events = append(events, Event{Type: "disconnected", At: now, PreviousTransport: old.Transport})
	}
	if !old.Connected && chosen != "" {
		kind := "connected"
		if e.everConnected {
			kind = "recovered"
		}
		events = append(events, Event{Type: kind, At: now, Transport: chosen})
		e.everConnected = true
	}
	if old.Connected && chosen != "" && chosen != old.Transport {
		events = append(events, Event{Type: "transport_changed", At: now, Transport: chosen, PreviousTransport: old.Transport})
	}
	if e.state.Snapshot != nil && old.Snapshot != nil && chosen != "" && old.Snapshot.BootID != "" && e.state.Snapshot.BootID != old.Snapshot.BootID {
		events = append(events, Event{Type: "device_restarted", At: now, Transport: chosen, BootID: e.state.Snapshot.BootID})
	}
	return events
}
