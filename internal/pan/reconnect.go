// Package pan implements a scoped, one-attempt reconnect for an existing PAN profile.
package pan

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
)

type Run func(context.Context, ...string) (string, error)

func Ensure(ctx context.Context, run Run, uuid, peer string) error {
	if !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(uuid) {
		return fmt.Errorf("invalid PAN connection UUID")
	}
	mac, err := net.ParseMAC(peer)
	if err != nil || len(mac) != 6 {
		return fmt.Errorf("invalid PAN peer MAC")
	}
	peer = mac.String()
	active, err := run(ctx, "-g", "UUID", "connection", "show", "--active")
	if err != nil {
		return err
	}
	for _, s := range strings.Split(active, "\n") {
		if strings.EqualFold(strings.TrimSpace(s), uuid) {
			return nil
		}
	}
	metadata, err := run(ctx, "--escape", "no", "-g", "bluetooth.bdaddr,bluetooth.type", "connection", "show", "uuid", uuid)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(metadata), "\n")
	if len(lines) != 2 || !strings.EqualFold(strings.TrimSpace(lines[0]), peer) || strings.TrimSpace(lines[1]) != "panu" {
		return fmt.Errorf("refusing to activate non-matching PAN peer/profile")
	}
	_, err = run(ctx, "--wait", "10", "connection", "up", "uuid", uuid)
	return err
}
