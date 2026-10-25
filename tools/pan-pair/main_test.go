package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type testBusObject struct {
	dbus.BusObject
	call func(context.Context, string, dbus.Flags, ...interface{}) *dbus.Call
}

func (o testBusObject) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...interface{}) *dbus.Call {
	return o.call(ctx, method, flags, args...)
}

func TestRestoreAdapterSecondContextSurvivesFirstDeadline(t *testing.T) {
	var names []string
	obj := testBusObject{call: func(ctx context.Context, method string, flags dbus.Flags, args ...interface{}) *dbus.Call {
		if method != "org.freedesktop.DBus.Properties.Set" || args[0] != "org.bluez.Adapter1" {
			t.Fatalf("unexpected call: %s %v", method, args)
		}
		name := args[1].(string)
		names = append(names, name)
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("restore context must be bounded")
		}
		if name == "Discoverable" {
			if args[2].(dbus.Variant).Value() != true {
				t.Fatal("lost original discoverable value")
			}
			<-ctx.Done()
			return &dbus.Call{Err: ctx.Err()}
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("second restore received expired context: %v", err)
		}
		if args[2].(dbus.Variant).Value() != false {
			t.Fatal("lost original pairable value")
		}
		return &dbus.Call{}
	}}
	err := restoreAdapter(obj, true, false, 10*time.Millisecond)
	if !reflect.DeepEqual(names, []string{"Discoverable", "Pairable"}) {
		t.Fatalf("restore calls: %v", names)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup failure must propagate: %v", err)
	}
}

func TestTrustPeerFlagRequiresExplicitOptIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		t.Run(fmt.Sprint(optIn), func(t *testing.T) {
			oldFlags, oldArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine, os.Args = oldFlags, oldArgs }()
			flag.CommandLine = flag.NewFlagSet("pan-pair-test", flag.ContinueOnError)
			// Invalid peer stops before any system-bus or hardware access.
			os.Args = []string{"pan-pair-test", "--peer", "invalid"}
			if optIn {
				os.Args = append(os.Args, "--trust-peer")
			}
			if err := run(); err == nil || err.Error() != "invalid Bluetooth peer MAC" {
				t.Fatalf("expected pre-bus validation failure, got %v", err)
			}
			f := flag.Lookup("trust-peer")
			if f == nil || f.DefValue != "false" || f.Value.String() != fmt.Sprint(optIn) {
				t.Fatalf("incorrect trust-peer flag: %v", f)
			}
		})
	}
}

func TestRestoreAdapterPreservesAllFailures(t *testing.T) {
	first, second := errors.New("discoverable failed"), errors.New("pairable failed")
	calls := 0
	obj := testBusObject{call: func(ctx context.Context, _ string, _ dbus.Flags, _ ...interface{}) *dbus.Call {
		calls++
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if calls == 1 {
			return &dbus.Call{Err: first}
		}
		return &dbus.Call{Err: second}
	}}
	err := restoreAdapter(obj, false, false, time.Second)
	if calls != 2 || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("calls=%d, err=%v", calls, err)
	}
}

func TestPairingLeavesExistingTrustUnchangedWithoutOptIn(t *testing.T) {
	calls := 0
	obj := testBusObject{call: func(context.Context, string, dbus.Flags, ...interface{}) *dbus.Call {
		calls++
		return &dbus.Call{}
	}}
	var out bytes.Buffer
	peer := dbus.ObjectPath("/org/bluez/hci0/dev_02_00_00_00_00_02")
	if err := finishPairing(context.Background(), obj, peer, false, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("without --trust-peer, existing trust must not be read or changed: %d calls", calls)
	}
	if want := "PAIRED; DEVICE-WIDE TRUST UNCHANGED " + string(peer) + "\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestPairingOptInLabelsDeviceWideTrust(t *testing.T) {
	peer := dbus.ObjectPath("/org/bluez/hci0/dev_02_00_00_00_00_02")
	for _, setErr := range []error{nil, errors.New("trust denied")} {
		t.Run(fmt.Sprint(setErr), func(t *testing.T) {
			calls := 0
			obj := testBusObject{call: func(_ context.Context, method string, _ dbus.Flags, args ...interface{}) *dbus.Call {
				calls++
				if method != "org.freedesktop.DBus.Properties.Set" || len(args) != 3 || args[0] != "org.bluez.Device1" || args[1] != "Trusted" || args[2].(dbus.Variant).Value() != true {
					t.Fatalf("unexpected trust mutation: %s %v", method, args)
				}
				return &dbus.Call{Err: setErr}
			}}
			var out bytes.Buffer
			err := finishPairing(context.Background(), obj, peer, true, &out)
			if !errors.Is(err, setErr) || calls != 1 {
				t.Fatalf("err=%v, calls=%d", err, calls)
			}
			if setErr != nil {
				if out.Len() != 0 {
					t.Fatalf("failed trust must not claim success: %q", out.String())
				}
				return
			}
			if want := "PAIRED; DEVICE-WIDE TRUST ENABLED (--trust-peer; NOT PAN-ONLY) " + string(peer) + "\n"; out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestAgentOnlyAuthorizesExplicitPeerAndPANServices(t *testing.T) {
	a, err := newAgent("hci0", "02:00:00:00:00:02")
	if err != nil {
		t.Fatal(err)
	}
	peer := dbus.ObjectPath("/org/bluez/hci0/dev_02_00_00_00_00_02")
	if e := a.RequestAuthorization(peer); e != nil {
		t.Fatal(e)
	}
	if e := a.RequestConfirmation(peer, 123456); e != nil {
		t.Fatal(e)
	}
	if e := a.AuthorizeService(peer, "00001116-0000-1000-8000-00805f9b34fb"); e != nil {
		t.Fatal(e)
	}
	for _, p := range []dbus.ObjectPath{"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF", "/org/bluez/hci1/dev_02_00_00_00_00_02", "/org/bluez/hci0/dev_02_00_00_00_00_02/extra"} {
		if e := a.RequestAuthorization(p); e == nil {
			t.Fatalf("unexpected authorization for %s", p)
		}
	}
	if e := a.AuthorizeService(peer, "0000110b-0000-1000-8000-00805f9b34fb"); e == nil {
		t.Fatal("unexpected audio authorization")
	}
	if _, e := a.RequestPinCode(peer); e == nil {
		t.Fatal("must not invent legacy PIN")
	}
	if _, e := a.RequestPasskey(peer); e == nil {
		t.Fatal("must not invent passkey")
	}
}
