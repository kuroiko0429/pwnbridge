package host

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"sort"
)

const BusName = "io.github.kuroiko.Pwnbridge1"
const BusInterface = BusName
const BusPath dbus.ObjectPath = "/io/github/kuroiko/Pwnbridge1"
const propertiesInterface = "org.freedesktop.DBus.Properties"

type busAPI struct {
	runtime *Runtime
	ctx     context.Context
}

func (a *busAPI) GetState() (string, *dbus.Error) {
	b, err := json.Marshal(a.runtime.Snapshot())
	if err != nil {
		return "", busError("Failed", err)
	}
	return string(b), nil
}
func (a *busAPI) SetFace(face string, ttl uint32) *dbus.Error {
	return a.call(protocol.Request{Op: "set_face", Face: face, TTLSeconds: ttl})
}
func (a *busAPI) SetMessage(message string, ttl uint32) *dbus.Error {
	return a.call(protocol.Request{Op: "set_message", Message: message, TTLSeconds: ttl})
}
func (a *busAPI) Reconnect() *dbus.Error {
	if err := a.ctx.Err(); err != nil {
		return busError("Failed", err)
	}
	a.runtime.Reconnect()
	return nil
}
func (a *busAPI) ClearOverride() *dbus.Error { return a.call(protocol.Request{Op: "clear_override"}) }
func (a *busAPI) call(req protocol.Request) *dbus.Error {
	if err := ValidateRequest(req); err != nil {
		return busError("InvalidArgs", err)
	}
	if err := a.runtime.RPC(a.ctx, req); err != nil {
		return busError("Failed", err)
	}
	return nil
}
func busError(kind string, err error) *dbus.Error {
	return dbus.NewError(BusInterface+".Error."+kind, []any{err.Error()})
}

type busProperties struct{ runtime *Runtime }

func (p *busProperties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface != BusInterface {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{iface})
	}
	all := Properties(p.runtime.Snapshot())
	v, ok := all[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{name})
	}
	return v, nil
}
func (p *busProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != BusInterface {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{iface})
	}
	return Properties(p.runtime.Snapshot()), nil
}
func (p *busProperties) Set(iface, name string, value dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", []any{"all Pwnbridge properties are read-only"})
}
func Properties(s State) map[string]dbus.Variant {
	face, message, mode := "", "", ""
	cpu, percent := 0.0, 0.0
	cpuValid, batteryAvailable := false, false
	if v := s.Snapshot; v != nil {
		face, message, mode = v.Face, v.Message, v.Mode
		if v.CPUTempC != nil {
			cpu = *v.CPUTempC
			cpuValid = s.Connected
		}
		if v.Battery.Percent != nil {
			percent = *v.Battery.Percent
			batteryAvailable = s.Connected && v.Battery.Available
		}
	}
	raw, _ := json.Marshal(s)
	values := map[string]any{"Connected": s.Connected, "Transport": s.Transport, "Face": face, "Message": message, "Mode": mode, "CPUTemperature": cpu, "CPUTemperatureValid": cpuValid, "BatteryPercent": percent, "BatteryAvailable": batteryAvailable, "StateJSON": string(raw), "LastError": s.LastError, "LastSeen": s.LastSeen}
	out := make(map[string]dbus.Variant, len(values))
	for k, v := range values {
		out[k] = dbus.MakeVariant(v)
	}
	return out
}

// ExportBus exports the RPC/property API before acquiring its public bus name.
// RunDaemon starts the ordered publisher separately, with an independently owned
// raw socket interrupt; a bare godbus Conn.Close cannot interrupt a blocked send.
func ExportBus(conn *dbus.Conn, r *Runtime, ctx context.Context) error {
	api := &busAPI{r, ctx}
	props := &busProperties{r}
	if err := conn.Export(api, BusPath, BusInterface); err != nil {
		return err
	}
	if err := conn.Export(props, BusPath, propertiesInterface); err != nil {
		return err
	}
	var descriptors []introspect.Property
	values := Properties(r.Snapshot())
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		descriptors = append(descriptors, introspect.Property{Name: name, Type: values[name].Signature().String(), Access: "read", Annotations: []introspect.Annotation{{Name: "org.freedesktop.DBus.Property.EmitsChangedSignal", Value: "true"}}})
	}
	node := &introspect.Node{Name: string(BusPath), Interfaces: []introspect.Interface{{Name: BusInterface, Methods: introspect.Methods(api), Properties: descriptors, Signals: []introspect.Signal{{Name: "Event", Args: []introspect.Arg{{Name: "json", Type: "s"}}}}}, {Name: propertiesInterface, Methods: introspect.Methods(props), Signals: []introspect.Signal{{Name: "PropertiesChanged", Args: []introspect.Arg{{Type: "s"}, {Type: "a{sv}"}, {Type: "as"}}}}}}}
	if err := conn.Export(introspect.NewIntrospectable(node), BusPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	result, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if result != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("session D-Bus name %s already owned", BusName)
	}
	return nil
}
