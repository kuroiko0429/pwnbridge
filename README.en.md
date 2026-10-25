# Pwnbridge

[日本語](README.md) | **English**

A Linux companion for Pwnagotchi: authenticated telemetry, temporary face/message overrides, and a session D-Bus API. Bluetooth PAN is preferred; an authenticated USB stream stays ready as a fallback. This is a daemon and CLI, not a graphical widget.

## What it does

- Reads the native face, message, mode, CPU temperature, uptime, and boot identity.
- Reads PiSugar battery percentage, voltage, charging, and external-power status through an existing local manager. Unknown sensor values remain `null` with errors, never fabricated zeroes.
- Overrides face and message independently for 1–300 seconds, with explicit clear.
- Tracks Bluetooth → USB → Bluetooth transitions, disconnect/recovery, and device restarts. Retained snapshots are marked stale when disconnected.
- Exposes CLI and D-Bus interfaces for your own desktop frontend.

```text
Linux PC: Go daemon + CLI + session D-Bus
  ├─ SSH over Bluetooth PAN → device 10.77.0.2      preferred
  └─ SSH over USB           → device 10.12.194.1   standby/fallback
                                  │
                         Go pwnbridge-agent (pi)
                           ├─ local CPU / uptime / boot ID
                           ├─ existing PiSugar Unix socket (read-only)
                           └─ Unix socket → minimal Python UI plugin
```

The host daemon, device agent, and PAN reconnect helper are Go. Only the bridge into Pwnagotchi's Python UI uses Python. No new HTTP/TCP server, Web UI, attack controls, shutdown API, firmware changes, or automatic kernel resets are added. The daemon does not configure or pair networks.

## Requirements

- Linux host with Go **1.24+**, GNU Make, Python **3.13+** for the installer and local tests, OpenSSH, systemd user services, and a **Unix-socket session D-Bus**. `dbus-run-session` is needed for the private-bus tests.
- Device: ARM64 Pwnagotchi with its existing `pi` account/group, key-authenticated SSH, a root-run Pwnagotchi service, and compatible UI SDK. The supported installation uses `/opt/.pwn/bin/python`; configuration editing also needs `tomlkit` there.
- Optional PAN: BlueZ and NetworkManager on both machines, working adapters and permitted pairing/profile management.
- Optional battery readings: an existing PiSugar manager exposing `/tmp/pisugar-server.sock`; Pwnbridge does not install a second power controller.

Known hardware: Raspberry Pi Zero 2 W, Waveshare 2.13-inch V3 display, and PiSugar 3. Other images, architectures, displays, and battery models need independent verification. This is not a blank-image installer.

## Build and install

Run on the Linux host:

```sh
git clone https://github.com/kuroiko0429/pwnbridge.git
cd pwnbridge
make build cross
make test check
make install-user
export PATH="$HOME/.local/bin:$PATH"
```

`make cross` builds ARM64 device binaries; compilation is not a hardware test. `make install-user` installs the host binary/user unit and creates a private initial config only if absent. It does not use sudo or enable/restart the service. Existing configuration is preserved: review its absolute private-key path before starting.

**Complete the [English installation guide](docs/install.en.md)** before enabling the daemon: verify the USB SSH host key, install the three device files, enable the UI plugin without replacing existing TOML, then optionally add PAN. [日本語の導入手順](docs/install.ja.md).

```sh
# After device setup, config review, and pinned-key SSH verification:
systemctl --user enable --now pwnbridge.service
pwnbridge status --json
```

## CLI

```sh
pwnbridge status
pwnbridge status --json
pwnbridge watch
pwnbridge watch --json
pwnbridge face --ttl 15s '(^_^)'
pwnbridge message --ttl 20s 'Hello from Linux'
pwnbridge face --ttl 15s -- '-_-'
pwnbridge clear
pwnbridge reconnect
```

Flags precede TEXT. TTL is a whole number of seconds (default `30s`); face/message limits are 64/512 Unicode characters. `reconnect` only restarts communication workers; it neither re-pairs Bluetooth nor changes routes. A successful `status` command does not by itself mean `connected=true`.

TTL expiry, clear, and unload restore the **last observed native value, best effort**, not an atomic snapshot of the latest concurrent UI update. Display refresh follows Pwnagotchi's normal cycle. A failure after UI application or state-file publication may return `UI request outcome indeterminate; restoration attempted`; inspect state rather than assuming nothing happened or automatically retrying on another link.

## Session D-Bus API

```text
Bus:        user/session (Unix socket only, not system bus)
Service:    io.github.kuroiko.Pwnbridge1
Object:     /io/github/kuroiko/Pwnbridge1
Interface:  io.github.kuroiko.Pwnbridge1
```

| Method | Arguments → result |
|---|---|
| `GetState` | none → string containing JSON |
| `SetFace` | string, uint32 TTL seconds → no result |
| `SetMessage` | string, uint32 TTL seconds → no result |
| `ClearOverride` | none → no result |
| `Reconnect` | none → no result |

Read-only properties: `Connected`, `Transport`, `Face`, `Message`, `Mode`, `CPUTemperature`, `CPUTemperatureValid`, `BatteryPercent`, `BatteryAvailable`, `StateJSON`, `LastError`, `LastSeen`.

Always pair numeric temperature/battery properties with their validity flag; D-Bus doubles cannot express JSON `null`. `StateJSON` retains nulls, per-sensor errors, snapshot freshness, and per-transport diagnostics. On disconnect, last values may remain but are not current.

Signals: standard `org.freedesktop.DBus.Properties.PropertiesChanged` and `Event(string JSON)` with types `connected`, `disconnected`, `recovered`, `transport_changed`, `device_restarted`. Notifications use a bounded FIFO of 64 batches and a 2-second publication deadline per batch. Overflow/send failure closes the bus and fails the daemon, allowing the user service to restart; this is not a durable event log. Restart `watch` after daemon loss.

```sh
busctl --user introspect io.github.kuroiko.Pwnbridge1 /io/github/kuroiko/Pwnbridge1
busctl --user call io.github.kuroiko.Pwnbridge1 /io/github/kuroiko/Pwnbridge1 io.github.kuroiko.Pwnbridge1 GetState
```

Details: [host API/configuration](docs/host.md) (Japanese), [device internals](device/README.ja.md) (Japanese), [PAN setup / trust boundary](docs/pan.md) (bilingual).

## Verification and limitations

Local tests exercise validation, partial telemetry, real subprocess cancellation/backpressure, private D-Bus sessions, and a Python Unix-socket bridge with a test-only SDK fixture. Hardware checks on the known setup covered authenticated USB/PAN telemetry, UI TTL/clear, fallback/recovery, and boot-identity changes. These do **not** establish a clean installation on a blank SD card, every image/SDK version, physical cable/range behavior, or every host reboot scenario.

PiSugar readings recovered after charging/waking the battery hardware. An empty/asleep PiSugar can stop responding over I²C even while USB powers the Pi. Missing readings are still errors/nulls; this project does not change charging, cutoff, or controller settings.

A USB transport stall recurred after a device reboot despite carrier/IP being present. A controlled device-side `g_ether` reset over working Bluetooth recovered that occurrence, but is **not a permanent fix** and is not automated. Inspect enumeration, interface/address, TCP/SSH, and authenticated frames separately; do not mark standby healthy from carrier alone or drop both recovery paths to test it.

## Local tests

`make test check` runs Go race tests / vet and Python tests. Additional tests for the documented TOML installation steps require `tomlkit`; only those tests are skipped when it is absent. CI installs `tomlkit` and executes them. Local tests do not require the device SDK, root privileges, or a hardware connection.

## License

GPL-3.0; see [LICENSE](LICENSE).
