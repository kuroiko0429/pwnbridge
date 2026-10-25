package host

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDefaultConfigPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with spaces 日本語")
	absoluteXDG := filepath.Join(t.TempDir(), "xdg with spaces 日本語")
	for _, tt := range []struct {
		name string
		xdg  string
		base string
	}{
		{name: "absolute_XDG_used", xdg: absoluteXDG, base: absoluteXDG},
		{name: "empty_XDG_falls_back_to_HOME", base: filepath.Join(home, ".config")},
		{name: "relative_XDG_falls_back_to_HOME", xdg: "relative/config", base: filepath.Join(home, ".config")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			want := filepath.Join(tt.base, "pwnbridge", "config.json")
			if got := DefaultConfigPath(); got != want {
				t.Fatalf("DefaultConfigPath() = %q, want %q", got, want)
			}
		})
	}
}

func goodConfig() Config {
	c := Config{Username: "pi", IdentityFile: "/home/test/.ssh/id_ed25519", HostKeyAlias: "10.12.194.1", RemoteAgent: "/usr/local/bin/pwnbridge-agent", Endpoints: []Endpoint{{Name: "usb", Address: "10.12.194.1"}}}
	c.defaults()
	return c
}
func TestConfigRejectsUnsafeValues(t *testing.T) {
	cases := []func(*Config){func(c *Config) { c.Username = "pi;id" }, func(c *Config) { c.RemoteAgent = "/bin/agent;id" }, func(c *Config) { c.Endpoints[0].Address = "-oProxyCommand=x" }, func(c *Config) { c.HostKeyAlias = "other" }, func(c *Config) { c.IdentityFile = "relative" }, func(c *Config) { c.Endpoints[0].Name = "wifi" }, func(c *Config) { c.Endpoints = nil }, func(c *Config) { c.Endpoints[0].Interface = "[bad" }, func(c *Config) { c.StaleAfter.Duration = c.PollInterval.Duration }, func(c *Config) { c.BluetoothHealthyFrames = -1 }}
	for i, mutate := range cases {
		c := goodConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if err := goodConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestConfigRejectsBroadcast(t *testing.T) {
	c := goodConfig()
	c.Endpoints[0].Address = "255.255.255.255"
	if c.Validate() == nil {
		t.Fatal("broadcast accepted")
	}
}
func TestConfigRejectsFIFOWithoutBlocking(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := LoadConfig(p); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("config load blocked on FIFO")
	}
}
func TestConfigSecureLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	body := `{"username":"pi","identity_file":"/home/test/.ssh/id_ed25519","host_key_alias":"10.12.194.1","remote_agent":"/usr/local/bin/pwnbridge-agent","endpoints":[{"name":"usb","address":"10.12.194.1","interface":"enp0s20f0u*"}]}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.StaleAfter.Duration <= c.PollInterval.Duration {
		t.Fatal("invalid defaults")
	}
	if err = os.Chmod(p, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(p); err == nil {
		t.Fatal("insecure config accepted")
	}
}
