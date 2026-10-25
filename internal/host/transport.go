package host

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
)

type LocalInterface struct {
	Name      string
	Up        bool
	Addresses []string
}
type Binding struct {
	Interface string
	Source    string
}

func LocalInterfaces() ([]LocalInterface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var rows []LocalInterface
	for _, i := range all {
		a, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		row := LocalInterface{Name: i.Name, Up: i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0}
		for _, v := range a {
			row.Addresses = append(row.Addresses, v.String())
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func MatchInterface(e Endpoint, rows []LocalInterface) (Binding, error) {
	target, err := netip.ParseAddr(e.Address)
	if err != nil {
		return Binding{}, err
	}
	for _, i := range rows {
		if !i.Up {
			continue
		}
		if e.Interface != "" {
			ok, err := filepath.Match(e.Interface, i.Name)
			if err != nil {
				return Binding{}, err
			}
			if !ok {
				continue
			}
		}
		for _, s := range i.Addresses {
			prefix, err := netip.ParsePrefix(s)
			if err == nil && prefix.Addr().Is4() && prefix.Contains(target) && prefix.Addr() != target {
				return Binding{Interface: i.Name, Source: prefix.Addr().String()}, nil
			}
		}
	}
	return Binding{}, fmt.Errorf("%s: no UP local interface matching %q with directly connected subnet for %s", e.Name, e.Interface, e.Address)
}
func GateEndpoint(e Endpoint) (Binding, error) {
	rows, err := LocalInterfaces()
	if err != nil {
		return Binding{}, err
	}
	return MatchInterface(e, rows)
}
func SSHArgs(c Config, e Endpoint, b Binding, operation string) []string {
	args := []string{"-F", "/dev/null", "-T", "-e", "none", "-i", c.IdentityFile, "-b", b.Source}
	for _, opt := range []string{"StrictHostKeyChecking=yes", "UpdateHostKeys=no", "CheckHostIP=no", "VerifyHostKeyDNS=no", "HostKeyAlias=" + c.HostKeyAlias, "BatchMode=yes", "IdentitiesOnly=yes", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "ConnectTimeout=3", "ConnectionAttempts=1", "ServerAliveInterval=2", "ServerAliveCountMax=2", "ControlMaster=no", "ControlPath=none", "ProxyCommand=none", "ProxyJump=none", "PermitLocalCommand=no", "ClearAllForwardings=yes", "ForwardAgent=no", "ForwardX11=no", "BindInterface=" + b.Interface} {
		args = append(args, "-o", opt)
	}
	args = append(args, "--", c.Username+"@"+e.Address, c.RemoteAgent, operation)
	if operation == "stream" {
		args = append(args, "--interval", c.PollInterval.String())
	}
	return args
}
