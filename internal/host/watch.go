package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/godbus/dbus/v5"
	"io"
	"time"
)

func watch(ctx context.Context, conn *dbus.Conn, out io.Writer, asJSON bool) error {
	signals := make(chan *dbus.Signal, 128)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	matches := [][]dbus.MatchOption{{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(BusPath), dbus.WithMatchInterface(BusInterface), dbus.WithMatchMember("Event")}, {dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(BusPath), dbus.WithMatchInterface(propertiesInterface), dbus.WithMatchMember("PropertiesChanged")}, {dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, BusName)}}
	for _, options := range matches {
		if err := conn.AddMatchSignal(options...); err != nil {
			return err
		}
		defer conn.RemoveMatchSignal(options...)
	}
	printState := func(raw string) error {
		var s State
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return fmt.Errorf("invalid StateJSON: %w", err)
		}
		if asJSON {
			b, err := json.Marshal(struct {
				Type  string `json:"type"`
				State State  `json:"state"`
			}{"state", s})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, string(b))
			return err
		}
		_, err := io.WriteString(out, FormatStatus(s))
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	var raw string
	err := conn.Object(BusName, BusPath).CallWithContext(bounded, BusInterface+".GetState", 0).Store(&raw)
	cancel()
	if err != nil {
		return err
	}
	if err = printState(raw); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-conn.Context().Done():
			return errors.New("session D-Bus disconnected")
		case sig, ok := <-signals:
			if !ok {
				return errors.New("D-Bus signal channel closed")
			}
			if sig == nil {
				continue
			}
			switch sig.Name {
			case propertiesInterface + ".PropertiesChanged":
				if len(sig.Body) != 3 {
					continue
				}
				iface, ok := sig.Body[0].(string)
				if !ok || iface != BusInterface {
					continue
				}
				changed, ok := sig.Body[1].(map[string]dbus.Variant)
				if !ok {
					continue
				}
				if v, ok := changed["StateJSON"]; ok {
					if raw, ok := v.Value().(string); ok {
						if err = printState(raw); err != nil {
							return err
						}
					}
				}
			case BusInterface + ".Event":
				if len(sig.Body) != 1 {
					continue
				}
				raw, ok := sig.Body[0].(string)
				if !ok {
					continue
				}
				if asJSON {
					_, err = fmt.Fprintln(out, raw)
				} else {
					var event Event
					if err = json.Unmarshal([]byte(raw), &event); err == nil {
						_, err = fmt.Fprintf(out, "[%s] %s %s\n", event.At.Format(time.RFC3339), event.Type, event.Transport)
					}
				}
				if err != nil {
					return err
				}
			case "org.freedesktop.DBus.NameOwnerChanged":
				if len(sig.Body) == 3 {
					old, _ := sig.Body[1].(string)
					if old != "" {
						return errors.New("pwnbridge daemon owner changed or exited; restart watch")
					}
				}
			}
		}
	}
}
