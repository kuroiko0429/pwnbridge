package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	d.Duration = v
	return err
}
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

type Endpoint struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Interface string `json:"interface"`
}
type Config struct {
	Username               string     `json:"username"`
	IdentityFile           string     `json:"identity_file"`
	HostKeyAlias           string     `json:"host_key_alias"`
	RemoteAgent            string     `json:"remote_agent"`
	Endpoints              []Endpoint `json:"endpoints"`
	PollInterval           Duration   `json:"poll_interval"`
	StaleAfter             Duration   `json:"stale_after"`
	RetryInterval          Duration   `json:"retry_interval"`
	IdleTimeout            Duration   `json:"idle_timeout"`
	RPCTimeout             Duration   `json:"rpc_timeout"`
	BluetoothHealthyFrames int        `json:"bluetooth_healthy_frames"`
}

func DefaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pwnbridge", "config.json")
}
func LoadConfig(path string) (Config, error) {
	var c Config
	// O_NONBLOCK prevents a mistakenly configured FIFO from hanging before f.Stat.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return c, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return c, errors.New("config must be a regular file with permissions 0600 or stricter")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return c, err
	}
	if len(b) > 65536 {
		return c, errors.New("config too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain exactly one JSON object")
	}
	c.defaults()
	return c, c.Validate()
}
func (c *Config) defaults() {
	if c.PollInterval.Duration == 0 {
		c.PollInterval.Duration = time.Second
	}
	if c.StaleAfter.Duration == 0 {
		c.StaleAfter.Duration = 5 * time.Second
	}
	if c.RetryInterval.Duration == 0 {
		c.RetryInterval.Duration = 2 * time.Second
	}
	if c.IdleTimeout.Duration == 0 {
		c.IdleTimeout.Duration = 4 * time.Second
	}
	if c.RPCTimeout.Duration == 0 {
		c.RPCTimeout.Duration = 8 * time.Second
	}
	if c.BluetoothHealthyFrames == 0 {
		c.BluetoothHealthyFrames = 3
	}
}

var safeUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var safePath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

func (c Config) Validate() error {
	if !safeUser.MatchString(c.Username) {
		return errors.New("invalid username")
	}
	if !filepath.IsAbs(c.IdentityFile) || strings.ContainsAny(c.IdentityFile, "\r\n\x00") {
		return errors.New("identity_file must be absolute")
	}
	if c.HostKeyAlias != "10.12.194.1" {
		return errors.New("host_key_alias must pin existing device 10.12.194.1")
	}
	if !safePath.MatchString(c.RemoteAgent) || filepath.Clean(c.RemoteAgent) != c.RemoteAgent {
		return errors.New("remote_agent must be an absolute conservative path")
	}
	if len(c.Endpoints) < 1 || len(c.Endpoints) > 2 {
		return errors.New("one or two endpoints required")
	}
	seen := map[string]bool{}
	for _, e := range c.Endpoints {
		if e.Name != "bluetooth" && e.Name != "usb" {
			return errors.New("endpoint name must be bluetooth or usb")
		}
		if seen[e.Name] {
			return errors.New("duplicate endpoint name")
		}
		seen[e.Name] = true
		a, err := netip.ParseAddr(e.Address)
		if err != nil || !a.Is4() || a.IsLoopback() || !(a.IsGlobalUnicast() || a.IsLinkLocalUnicast()) {
			return errors.New("endpoint address must be unicast IPv4")
		}
		if _, err := filepath.Match(e.Interface, ""); err != nil || strings.ContainsAny(e.Interface, "/\r\n\x00") {
			return errors.New("invalid interface glob")
		}
	}
	if c.PollInterval.Duration < 100*time.Millisecond || c.PollInterval.Duration > time.Minute {
		return errors.New("poll_interval must be 100ms..1m")
	}
	if c.StaleAfter.Duration <= c.PollInterval.Duration || c.StaleAfter.Duration > 5*time.Minute {
		return errors.New("stale_after must exceed poll_interval and be <=5m")
	}
	if c.IdleTimeout.Duration <= c.PollInterval.Duration || c.IdleTimeout.Duration > 5*time.Minute {
		return errors.New("idle_timeout must exceed poll_interval and be <=5m")
	}
	if c.RetryInterval.Duration < 100*time.Millisecond || c.RetryInterval.Duration > time.Minute {
		return errors.New("retry_interval must be 100ms..1m")
	}
	if c.RPCTimeout.Duration < time.Second || c.RPCTimeout.Duration > time.Minute {
		return errors.New("rpc_timeout must be 1s..1m")
	}
	if c.BluetoothHealthyFrames < 1 || c.BluetoothHealthyFrames > 100 {
		return errors.New("bluetooth_healthy_frames must be 1..100")
	}
	return nil
}
