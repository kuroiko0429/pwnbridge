// pan-pair is a short-lived, peer-allowlisted setup helper, not a general pairing agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

type agent struct{ peer dbus.ObjectPath }

func rejected() *dbus.Error {
	return dbus.NewError("org.bluez.Error.Rejected", []any{"Pwnbridge setup only authorizes its configured PAN peer"})
}
func newAgent(adapter, mac string) (*agent, error) {
	if !validAdapter(adapter) {
		return nil, errors.New("adapter must be hci followed by digits")
	}
	parsed, err := net.ParseMAC(mac)
	if err != nil || len(parsed) != 6 {
		return nil, errors.New("invalid Bluetooth peer MAC")
	}
	return &agent{dbus.ObjectPath("/org/bluez/" + adapter + "/dev_" + strings.ReplaceAll(strings.ToUpper(parsed.String()), ":", "_"))}, nil
}
func validAdapter(s string) bool {
	if !strings.HasPrefix(s, "hci") || len(s) == 3 {
		return false
	}
	for _, r := range s[3:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (a *agent) allow(p dbus.ObjectPath) *dbus.Error {
	if p != a.peer {
		return rejected()
	}
	return nil
}
func (a *agent) Release() *dbus.Error                                             { return nil }
func (a *agent) Cancel() *dbus.Error                                              { return nil }
func (a *agent) RequestPinCode(p dbus.ObjectPath) (string, *dbus.Error)           { return "", rejected() }
func (a *agent) RequestPasskey(p dbus.ObjectPath) (uint32, *dbus.Error)           { return 0, rejected() }
func (a *agent) DisplayPinCode(p dbus.ObjectPath, _ string) *dbus.Error           { return a.allow(p) }
func (a *agent) DisplayPasskey(p dbus.ObjectPath, _ uint32, _ uint16) *dbus.Error { return a.allow(p) }
func (a *agent) RequestConfirmation(p dbus.ObjectPath, _ uint32) *dbus.Error      { return a.allow(p) }
func (a *agent) RequestAuthorization(p dbus.ObjectPath) *dbus.Error               { return a.allow(p) }
func (a *agent) AuthorizeService(p dbus.ObjectPath, uuid string) *dbus.Error {
	if e := a.allow(p); e != nil {
		return e
	}
	switch strings.ToLower(uuid) {
	case "00001115-0000-1000-8000-00805f9b34fb", "00001116-0000-1000-8000-00805f9b34fb", "00001117-0000-1000-8000-00805f9b34fb":
		return nil
	}
	return rejected()
}
func getBool(ctx context.Context, obj dbus.BusObject, name string) (bool, error) {
	var v dbus.Variant
	if err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.bluez.Adapter1", name).Store(&v); err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("invalid %s property", name)
	}
	return b, nil
}
func setAdapter(ctx context.Context, obj dbus.BusObject, name string, value bool) error {
	return obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0, "org.bluez.Adapter1", name, dbus.MakeVariant(value)).Err
}
func restoreAdapter(obj dbus.BusObject, oldDiscoverable, oldPairable bool, timeout time.Duration) error {
	var result error
	for _, property := range []struct {
		name  string
		value bool
	}{{"Discoverable", oldDiscoverable}, {"Pairable", oldPairable}} {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := setAdapter(ctx, obj, property.name, property.value)
		cancel()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("restore %s: %w", property.name, err))
		}
	}
	return result
}
func paired(ctx context.Context, obj dbus.BusObject) bool {
	var v dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.bluez.Device1", "Paired").Store(&v)
	if err != nil {
		return false
	}
	b, _ := v.Value().(bool)
	return b
}
func finishPairing(ctx context.Context, dev dbus.BusObject, peer dbus.ObjectPath, trustPeer bool, out io.Writer) error {
	if !trustPeer {
		fmt.Fprintln(out, "PAIRED; DEVICE-WIDE TRUST UNCHANGED", peer)
		return nil
	}
	if err := dev.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0, "org.bluez.Device1", "Trusted", dbus.MakeVariant(true)).Err; err != nil {
		return err
	}
	fmt.Fprintln(out, "PAIRED; DEVICE-WIDE TRUST ENABLED (--trust-peer; NOT PAN-ONLY)", peer)
	return nil
}

func run() (err error) {
	peer := flag.String("peer", "", "exact Bluetooth peer MAC")
	adapter := flag.String("adapter", "hci0", "local controller")
	initiate := flag.Bool("initiate", false, "discover and initiate pairing")
	trustPeer := flag.Bool("trust-peer", false, "persist DEVICE-WIDE BlueZ trust for the exact peer (NOT PAN-only); default leaves existing trust unchanged")
	discoverable := flag.Bool("discoverable", false, "temporarily make adapter discoverable")
	duration := flag.Duration("timeout", 90*time.Second, "bounded setup window")
	flag.Parse()
	if *duration < time.Second || *duration > 3*time.Minute {
		return errors.New("timeout must be 1s..3m")
	}
	a, err := newAgent(*adapter, *peer)
	if err != nil {
		return err
	}
	sigctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sigctx, *duration)
	defer cancel()
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	path := dbus.ObjectPath("/io/github/kuroiko/PwnbridgePair1")
	if err = conn.Export(a, path, "org.bluez.Agent1"); err != nil {
		return err
	}
	node := &introspect.Node{Interfaces: []introspect.Interface{{Name: "org.bluez.Agent1", Methods: introspect.Methods(a)}}}
	if err = conn.Export(introspect.NewIntrospectable(node), path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	mgr := conn.Object("org.bluez", "/org/bluez")
	if err = mgr.CallWithContext(ctx, "org.bluez.AgentManager1.RegisterAgent", 0, path, "NoInputNoOutput").Err; err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		mgr.CallWithContext(c, "org.bluez.AgentManager1.UnregisterAgent", 0, path)
	}()
	if !*initiate {
		if err = mgr.CallWithContext(ctx, "org.bluez.AgentManager1.RequestDefaultAgent", 0, path).Err; err != nil {
			return err
		}
	}
	ad := conn.Object("org.bluez", dbus.ObjectPath("/org/bluez/"+*adapter))
	oldPairable, err := getBool(ctx, ad, "Pairable")
	if err != nil {
		return err
	}
	oldDiscoverable, err := getBool(ctx, ad, "Discoverable")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, restoreAdapter(ad, oldDiscoverable, oldPairable, 3*time.Second))
	}()
	if err = setAdapter(ctx, ad, "Pairable", true); err != nil {
		return err
	}
	if *discoverable {
		if err = setAdapter(ctx, ad, "Discoverable", true); err != nil {
			return err
		}
	}
	dev := conn.Object("org.bluez", a.peer)
	fmt.Println("PAIR WINDOW READY; allowlisted peer", strings.ToUpper(*peer))
	if *initiate && !paired(ctx, dev) {
		if err = ad.CallWithContext(ctx, "org.bluez.Adapter1.StartDiscovery", 0).Err; err != nil {
			return err
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ad.CallWithContext(c, "org.bluez.Adapter1.StopDiscovery", 0)
		}()
		for {
			var v dbus.Variant
			err = dev.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.bluez.Device1", "Address").Store(&v)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		if err = dev.CallWithContext(ctx, "org.bluez.Device1.Pair", 0).Err; err != nil {
			return err
		}
	}
	for !paired(ctx, dev) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return finishPairing(ctx, dev, a.peer, *trustPeer, os.Stdout)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
