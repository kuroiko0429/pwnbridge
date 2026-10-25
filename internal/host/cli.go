package host

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/godbus/dbus/v5"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"io"
	"strings"
	"time"
)

const Usage = "pwnbridge daemon [--config PATH] | status [--json] | watch [--json] | face [--ttl 30s] TEXT | message [--ttl 30s] TEXT | clear | reconnect"

type cliOptions struct {
	command, config string
	json            bool
	request         protocol.Request
}

func parseCLI(args []string, errout io.Writer) (cliOptions, error) {
	var o cliOptions
	if len(args) == 0 {
		return o, errors.New(Usage)
	}
	o.command = args[0]
	f := flag.NewFlagSet("pwnbridge "+o.command, flag.ContinueOnError)
	f.SetOutput(errout)
	ttl := 30 * time.Second
	switch o.command {
	case "daemon":
		f.StringVar(&o.config, "config", DefaultConfigPath(), "configuration file")
	case "status", "watch":
		f.BoolVar(&o.json, "json", false, "JSON output")
	case "face", "message":
		f.DurationVar(&ttl, "ttl", 30*time.Second, "override TTL (whole seconds, 1s..300s)")
	case "clear", "reconnect", "version":
	default:
		return o, fmt.Errorf("unknown command %q; %s", o.command, Usage)
	}
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if o.command == "face" || o.command == "message" {
		if f.NArg() != 1 {
			return o, errors.New("expected exactly one TEXT argument; flags must precede TEXT")
		}
		if ttl < time.Second || ttl > 300*time.Second || ttl%time.Second != 0 {
			return o, errors.New("ttl must be whole seconds in 1s..300s")
		}
		o.request.TTLSeconds = uint32(ttl / time.Second)
		if o.command == "face" {
			o.request.Op = "set_face"
			o.request.Face = f.Arg(0)
		} else {
			o.request.Op = "set_message"
			o.request.Message = f.Arg(0)
		}
		if err := ValidateRequest(o.request); err != nil {
			return o, err
		}
	} else if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	return o, nil
}
func RunCLI(ctx context.Context, args []string, out, errout io.Writer) error {
	o, err := parseCLI(args, errout)
	if err != nil {
		return err
	}
	if o.command == "version" {
		_, err = fmt.Fprintln(out, "pwnbridge schema 1")
		return err
	}
	if o.command == "daemon" {
		c, err := LoadConfig(o.config)
		if err != nil {
			return err
		}
		return RunDaemon(ctx, c)
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("session D-Bus: %w", err)
	}
	defer conn.Close()
	obj := conn.Object(BusName, BusPath)
	call := func(method string, args ...any) *dbus.Call {
		bounded, cancel := context.WithTimeout(ctx, 65*time.Second)
		defer cancel()
		return obj.CallWithContext(bounded, BusInterface+"."+method, 0, args...)
	}
	switch o.command {
	case "status":
		var raw string
		if err = call("GetState").Store(&raw); err != nil {
			return fmt.Errorf("GetState (is pwnbridge daemon running?): %w", err)
		}
		if o.json {
			_, err = fmt.Fprintln(out, raw)
			return err
		}
		var s State
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return err
		}
		_, err = io.WriteString(out, FormatStatus(s))
		return err
	case "face":
		return call("SetFace", o.request.Face, o.request.TTLSeconds).Err
	case "message":
		return call("SetMessage", o.request.Message, o.request.TTLSeconds).Err
	case "clear":
		return call("ClearOverride").Err
	case "reconnect":
		return call("Reconnect").Err
	case "watch":
		return watch(ctx, conn, out, o.json)
	}
	return errors.New("unreachable command")
}
func FormatStatus(s State) string {
	var b strings.Builder
	connection := "DISCONNECTED"
	if s.Connected {
		connection = "CONNECTED " + s.Transport
	}
	if s.Stale {
		connection += " (STALE: last values are not current)"
	}
	fmt.Fprintln(&b, connection)
	if s.LastSeen != "" {
		fmt.Fprintln(&b, "Last seen:", s.LastSeen)
	}
	if s.LastError != "" {
		fmt.Fprintln(&b, "Error:", s.LastError)
	}
	if v := s.Snapshot; v != nil {
		fmt.Fprintf(&b, "Face: %s\nMessage: %s\nMode: %s\n", v.Face, v.Message, v.Mode)
		if v.CPUTempC != nil {
			fmt.Fprintf(&b, "CPU: %.1f C\n", *v.CPUTempC)
		} else {
			fmt.Fprintln(&b, "CPU: unknown")
		}
		if v.CPUError != "" {
			fmt.Fprintln(&b, "CPU error:", v.CPUError)
		}
		if v.Battery.Available && v.Battery.Percent != nil {
			fmt.Fprintf(&b, "Battery: %.1f%%\n", *v.Battery.Percent)
		} else {
			fmt.Fprintln(&b, "Battery: unknown")
		}
		if v.Battery.Model != "" {
			fmt.Fprintln(&b, "Battery model:", v.Battery.Model)
		}
		if v.Battery.Error != "" {
			fmt.Fprintln(&b, "Battery error:", v.Battery.Error)
		}
		if v.UIError != "" {
			fmt.Fprintln(&b, "UI error:", v.UIError)
		}
	} else {
		fmt.Fprintln(&b, "No telemetry received; CPU: unknown; Battery: unknown")
	}
	return b.String()
}
