package host

import (
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"testing"
	"time"
)

func snap(boot string) protocol.Snapshot {
	return protocol.Snapshot{SchemaVersion: 1, BootID: boot, Hostname: "zero", SampledAt: time.Unix(1, 0)}
}
func TestDeviceRestartRetiresOldStandbyBoot(t *testing.T) {
	c := goodConfig()
	c.BluetoothHealthyFrames = 1
	e := NewEngine(c)
	now := time.Unix(100, 0)
	e.Begin("usb", 1, now)
	e.Begin("bluetooth", 1, now)
	e.Frame("bluetooth", 1, snap("old"), now)
	e.Frame("usb", 1, snap("old"), now)
	ev := e.Frame("usb", 1, snap("new"), now.Add(time.Second))
	if e.State().Snapshot.BootID != "new" || len(ev) != 2 || ev[1].Type != "device_restarted" {
		t.Fatal(e.State(), ev)
	}
	e.Frame("bluetooth", 1, snap("old"), now.Add(2*time.Second))
	if e.State().Snapshot.BootID != "new" {
		t.Fatal("retired boot revived")
	}
	ev = e.Frame("bluetooth", 1, snap("new"), now.Add(3*time.Second))
	if len(ev) != 1 || ev[0].Type != "transport_changed" {
		t.Fatal(ev)
	}
}
func TestStaleRecoveryRestartAndGeneration(t *testing.T) {
	c := goodConfig()
	e := NewEngine(c)
	now := time.Unix(100, 0)
	e.Begin("usb", 1, now)
	e.Frame("usb", 1, snap("a"), now)
	ev := e.Tick(now.Add(c.StaleAfter.Duration))
	s := e.State()
	if s.Connected || !s.Stale || s.Snapshot.BootID != "a" || s.LastError == "" || len(ev) != 1 || ev[0].Type != "disconnected" {
		t.Fatal(s, ev)
	}
	now = now.Add(10 * time.Second)
	e.Begin("usb", 2, now)
	e.Frame("usb", 1, snap("bad"), now)
	if e.State().Connected {
		t.Fatal("old generation revived")
	}
	ev = e.Frame("usb", 2, snap("b"), now)
	if len(ev) != 2 || ev[0].Type != "recovered" || ev[1].Type != "device_restarted" {
		t.Fatal(ev)
	}
	e.Fail("usb", 2, "EOF", now)
	e.Frame("usb", 2, snap("c"), now)
	if e.State().Connected {
		t.Fatal("failed generation revived")
	}
}
func TestSelectBluetoothThenUSBFallback(t *testing.T) {
	c := goodConfig()
	e := NewEngine(c)
	now := time.Unix(100, 0)
	e.Begin("usb", 1, now)
	e.Begin("bluetooth", 1, now)
	e.Frame("usb", 1, snap("a"), now)
	if e.State().Transport != "usb" {
		t.Fatal(e.State())
	}
	for i := 0; i < c.BluetoothHealthyFrames; i++ {
		e.Frame("bluetooth", 1, snap("a"), now)
	}
	if e.State().Transport != "bluetooth" {
		t.Fatal(e.State())
	}
	ev := e.Fail("bluetooth", 1, "EOF", now)
	if !e.State().Connected || e.State().Transport != "usb" {
		t.Fatal(e.State())
	}
	if len(ev) != 1 || ev[0].Type != "transport_changed" {
		t.Fatal(ev)
	}
}
