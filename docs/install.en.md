# Installation (Linux host + ARM64 Pwnagotchi)

[日本語](install.ja.md) | [English README](../README.en.md)

These are installation instructions, not a claim that a blank image was tested. Start from a functioning Pwnagotchi image with an existing `pi` account/group, working USB gadget networking, and authorized sudo access on the device. Do not create an extra administrator account to bypass image differences. Back up the SD card/configuration and retain a working USB recovery path. Only manage devices and networks you own or are authorized to use.

Host and device commands are labelled separately. Nothing below is a command to run against your currently installed service automatically. Network/service changes are explicit; read each section before applying it. A device reboot is not required just to install these files.

## 1. Build on the host

Install Go 1.24+, GNU Make, Python 3.13+, OpenSSH, Git, systemd, and D-Bus tools using your distribution. `make test` uses the race detector (a supported native Go toolchain/C compiler may be needed). PAN additionally needs BlueZ and NetworkManager on both systems. Run as your normal desktop user:

```sh
git clone https://github.com/kuroiko0429/pwnbridge.git
cd pwnbridge
make build cross
make test check
make install-user
export PATH="$HOME/.local/bin:$PATH"
```

The host installer uses no sudo, preserves an existing config, and does not start/restart a service. It creates the first config with a HOME-derived absolute identity path and mode 0600. Make sure `$HOME/.local/bin` is in your login PATH. ARM64 outputs are `bin/pwnbridge-agent.arm64`, `bin/pwnbridge-pan.arm64`, and `bin/pwnbridge-pan-pair.arm64`; no binary release download is required.

## 2. Verify USB and pin the SSH identity

Defaults are device `10.12.194.1/28`, host `10.12.194.2/28`. Use the data-capable USB port/cable, not the power-only connector. Keep existing working profiles and Internet sharing. See [USB/network notes below](#usb-networkmanager-notes) if an address is missing.

On a trusted device console (or another already authenticated channel), obtain its fingerprint:

```sh
# Device, trusted console:
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Enroll only the matching fingerprint in the host user's default known_hosts. Do not use `ssh-keyscan` alone as proof of identity. A changed key is a reason to investigate, not delete the old entry blindly. Use an existing authorized SSH key; arrange public-key enrollment through your image's trusted setup if needed. Never copy the private key to the device. For a passphrase-protected key, load it into your normal ssh-agent and ensure the user service can reach that agent.

```sh
# Host / PC: replace only the private-key filename if needed.
IDENTITY="$HOME/.ssh/id_ed25519"
TARGET=pi@10.12.194.1
# Interactive first enrollment: compare the shown fingerprint BEFORE accepting.
ssh -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes \
  -o HostKeyAlias=10.12.194.1 -o StrictHostKeyChecking=ask "$TARGET" true
# All following connections must work non-interactively with the pinned key.
ssh -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 "$TARGET" id
```

The daemon deliberately ignores personal ssh_config (`-F /dev/null`), disallows password fallback, and uses the default known_hosts location. Reproduce that behavior when testing authentication. Both USB and PAN must use `HostKeyAlias=10.12.194.1`; the current config validator requires this exact alias.

## 3. Copy and install the three core device files

```sh
# Host / PC, repository root; use the IDENTITY and TARGET from step 2.
ssh -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  "$TARGET" 'mkdir -p "$HOME/pwnbridge-install" && chmod 700 "$HOME/pwnbridge-install"'
scp -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  bin/pwnbridge-agent.arm64 device/pwnbridge.py device/pwnbridge.tmpfiles.conf \
  "$TARGET:pwnbridge-install/"
```

Open a pinned-key USB SSH shell using the same options. The following device-side installer backs up the config and any replaced files under a unique root-private `/var/backups/pwnbridge-*` directory. It reads and preserves the existing `main.custom_plugins` directory and other TOML settings/comments, enables only `main.plugins.pwnbridge.enabled`, and keeps existing radio/plugin flags unchanged. Every path component, from the filesystem root through the custom directory, must be a real directory (no symlinks), root-owned and not group/other writable. Unsafe paths require manual inspection/remediation; the installer does not recursively chown them. Before backing up or installing files, it reparses the edited TOML and aborts if its settings differ from the intended document. Review custom paths before installing; do not change ownership recursively on an unrelated directory.

```sh
# Device / 本体: run in the pi SSH shell, NOT on the host.
cd "$HOME/pwnbridge-install"
id pi
getent group pi
sudo /opt/.pwn/bin/python -c 'import pwnagotchi.plugins, tomlkit'
sudo /opt/.pwn/bin/python - <<'PY'
import datetime
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import tomlkit

stage = Path.cwd()
config = Path('/etc/pwnagotchi/config.toml')
if not config.is_file() or config.is_symlink():
    raise SystemExit('Expected an existing regular config.toml; inspect the image first')
doc = tomlkit.parse(config.read_text())
main = doc.setdefault('main', tomlkit.table())
custom = str(main.get('custom_plugins') or '/usr/local/share/pwnagotchi/custom-plugins')
plugin_dir = Path(custom)
if not plugin_dir.is_absolute():
    raise SystemExit('Existing custom_plugins must be an absolute path; inspect manually')
if '..' in plugin_dir.parts:
    raise SystemExit('Inspect custom_plugins manually: parent traversal is not allowed')
# Validate from the filesystem root down, before creating anything below it.
for component in reversed((plugin_dir, *plugin_dir.parents)):
    try:
        info = component.lstat()
    except FileNotFoundError:
        component.mkdir(mode=0o755)
        info = component.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise SystemExit('Unsafe custom plugin path component: ' + str(component) +
                         '; inspect/remediate manually (no symlinks, root-owned, no group/other writes)')

files = [
    ('pwnbridge-agent.arm64', Path('/usr/local/bin/pwnbridge-agent'), '0755'),
    ('pwnbridge.tmpfiles.conf', Path('/etc/tmpfiles.d/pwnbridge.conf'), '0644'),
    ('pwnbridge.py', plugin_dir / 'pwnbridge.py', '0644'),
]
for name, target, mode in files:
    if not (stage / name).is_file():
        raise SystemExit('Missing staged file: ' + name)
if not main.get('custom_plugins'):
    main['custom_plugins'] = custom
plugins = main.get('plugins', {})
if 'pwnbridge' in plugins:
    plugins['pwnbridge']['enabled'] = True
else:
    # A dotted key avoids introducing a table header among existing dotted keys.
    main[tomlkit.key(['plugins', 'pwnbridge', 'enabled'])] = True
expected = doc.unwrap()
serialized = tomlkit.dumps(doc)
try:
    reparsed = tomlkit.parse(serialized).unwrap()
except Exception as error:
    raise SystemExit('TOML serialization failed; original config unchanged: ' + str(error))
if reparsed != expected:
    raise SystemExit('TOML serialization changed settings; original config unchanged')
backup = Path(tempfile.mkdtemp(prefix='pwnbridge-' + datetime.datetime.now().strftime('%Y%m%d-'), dir='/var/backups'))
os.chmod(backup, 0o700)
shutil.copy2(config, backup / 'config.toml')
for name, target, mode in files:
    if target.exists():
        shutil.copy2(target, backup / target.name)
    subprocess.run(['install', '-o', 'root', '-g', 'root', '-m', mode,
                    str(stage / name), str(target)], check=True)
fd, temporary = tempfile.mkstemp(prefix='.pwnbridge-', dir=str(config.parent))
try:
    with os.fdopen(fd, 'w') as stream:
        os.fchmod(stream.fileno(), stat.S_IMODE(config.stat().st_mode))
        stream.write(serialized)
    os.replace(temporary, config)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
subprocess.run(['systemd-tmpfiles', '--create', '/etc/tmpfiles.d/pwnbridge.conf'], check=True)
print('Plugin directory:', plugin_dir)
print('Backup:', backup)
PY
```

This installs only the agent, plugin and tmpfiles definition. The agent needs no standalone system service: it runs as `pi` under SSH. The Python worker belongs to Pwnagotchi. Verify the actual Python SDK, inspect the updated TOML and compare it with the printed backup before restarting:

```sh
# Device: review configuration locally; do not publish it.
sudo /opt/.pwn/bin/python - <<'PY'
from pathlib import Path
import ast
import tomlkit
c = tomlkit.parse(Path('/etc/pwnagotchi/config.toml').read_text())
p = Path(c['main']['custom_plugins']) / 'pwnbridge.py'
ast.parse(p.read_text())
print('Plugin:', p)
print('Enabled:', c['main']['plugins']['pwnbridge']['enabled'])
PY
# Restart only after confirming USB recovery is available:
sudo systemctl restart pwnagotchi.service
sudo journalctl -u pwnagotchi.service -n 40 --no-pager
stat -c '%U %G %a %n' /run/pwnbridge /run/pwnbridge/control.sock /run/pwnbridge/ui.json
/usr/local/bin/pwnbridge-agent status
```

Expected permissions: `/run/pwnbridge` root:root **0755**, `control.sock` root:pi **0660**, `ui.json` root-owned **0644**. Do not loosen the socket to 0666. A successful process start is not proof of live UI: check `ui_available`, `ui_error`, and a current snapshot. Keep Web UI, reporting, updates and active radio operations disabled if already disabled; enabling this plugin is not a reason to change them.

## 4. Review the private host config

```sh
# Host / PC: review, do not overwrite an existing configuration blindly.
python3 - <<'PY'
import json
import os
from pathlib import Path
xdg = Path(os.environ.get('XDG_CONFIG_HOME') or '.')
base = xdg if xdg.is_absolute() else Path.home() / '.config'
p = base / 'pwnbridge/config.json'
c = json.loads(p.read_text())
print(json.dumps(c, indent=2))
print('Private key exists:', Path(c['identity_file']).is_file())
print('Config path:', p)
PY
```

`XDG_CONFIG_HOME` is used only when absolute; unset, empty or relative values fall back to `$HOME/.config` in both inspection and permission commands. Edit the printed file with your editor; do not replace an existing working config just to match the example. `identity_file` must be a real **absolute** path; literal `~` and `$HOME` inside JSON are not expanded. The placeholder below is intentionally not a usable path. Keep `username=pi` for the plugin's root:pi socket boundary and keep the exact host-key alias:

```json
{
  "username": "pi",
  "identity_file": "/absolute/path/to/your/private_key",
  "host_key_alias": "10.12.194.1",
  "remote_agent": "/usr/local/bin/pwnbridge-agent",
  "endpoints": [
    {"name": "bluetooth", "address": "10.77.0.2", "interface": "pwnpan0"},
    {"name": "usb", "address": "10.12.194.1", "interface": ""}
  ],
  "poll_interval": "1s",
  "stale_after": "5s",
  "retry_interval": "2s",
  "idle_timeout": "4s",
  "rpc_timeout": "8s",
  "bluetooth_healthy_frames": 3
}
```

An empty USB `interface` finds an UP local interface in the directly connected subnet, avoiding a machine-specific interface name. With ambiguous/overlapping subnets, specify the actual interface instead. For USB-only operation remove the Bluetooth endpoint. The selected transport still must deliver valid authenticated SSH telemetry; IP/carrier alone is insufficient.

```sh
# Host: use the printed path if XDG_CONFIG_HOME is customized.
python3 - <<'PY'
import os
from pathlib import Path
xdg = Path(os.environ.get('XDG_CONFIG_HOME') or '.')
base = xdg if xdg.is_absolute() else Path.home() / '.config'
(base / 'pwnbridge/config.json').chmod(0o600)
PY
```

## 5. Optional Bluetooth PAN

Follow the bilingual [PAN guide](pan.md). It includes MAC discovery, bounded pairing with explicit device-wide trust consent, manual host NAP `10.77.0.1/30` / device PANU `10.77.0.2/30`, and a reconnect timer configured with the **actual discovered** profile UUID.

After PAN connects, verify the same pinned device key (host):

```sh
ssh -F /dev/null -i "$IDENTITY" -o BatchMode=yes -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  pi@10.77.0.2 /usr/local/bin/pwnbridge-agent status
```

PAN is management-only: it supplies no Internet gateway/DNS. Keep the host Wi-Fi/default route and existing USB Internet sharing intact.

## 6. Start and verify the host service

```sh
# Host / PC, after device and SSH verification:
systemctl --user enable --now pwnbridge.service
systemctl --user status pwnbridge.service
pwnbridge status --json
pwnbridge watch --json
# Separate terminal:
pwnbridge message --ttl 5s 'Hello / こんにちは'
pwnbridge clear
journalctl --user -u pwnbridge.service -n 40 --no-pager
```

Check `connected`, `transport`, `stale`, and both transport errors in JSON. A battery error need not break CPU/UI. If this was an upgrade and the service was already running, `enable --now` does not necessarily replace its process: restart explicitly only when you intend to activate the new binary/config. Manage it as the desktop user, not with `sudo systemctl`.

```sh
systemctl --user restart pwnbridge.service   # explicit upgrade/config activation
systemctl --user stop pwnbridge.service
```

Stopping it does not delete network profiles or shut down the device. Do not validate installation by dropping both links. Test one transport only after confirming another recovery path. Hardware/cable tests are separate from the local test suite.

## USB NetworkManager notes

Only use nmcli on a machine that actually manages this interface with NetworkManager. Do not enable a second network manager alongside an image's existing networking. Discover profiles before adding anything:

```sh
# Each machine separately / 双方で個別に確認
ip -br address
ip route show default
nmcli device status
nmcli -f NAME,UUID,TYPE,DEVICE connection show
# Substitute a UUID discovered above / 上で確認した UUID を指定
USB_PROFILE_UUID='REPLACE_WITH_DISCOVERED_USB_PROFILE_UUID'
nmcli connection show uuid "$USB_PROFILE_UUID"
```

Set `USB_PROFILE_UUID` to the UUID from the listing; it is not a published constant. Inspect profile addressing, gateway, DNS and `ipv4.method` first. Back up the corresponding connection file before editing it; system connection files may contain secrets. Keep an existing shared profile rather than changing it to manual. These optional examples create **management-only** profiles only where no appropriate profile exists:

```sh
# Host ONLY if no appropriate USB profile exists; replace the interface name.
USB_IFACE='REPLACE_WITH_HOST_USB_INTERFACE'
nmcli connection add type ethernet ifname "$USB_IFACE" con-name pwnbridge-usb \
  ipv4.method manual ipv4.addresses 10.12.194.2/28 \
  ipv4.never-default yes ipv6.method disabled
nmcli --wait 15 connection up pwnbridge-usb
# Device ONLY if it uses NetworkManager and has no USB profile.
sudo nmcli connection add type ethernet ifname usb0 con-name pwnbridge-usb \
  ipv4.method manual ipv4.addresses 10.12.194.1/28 \
  ipv4.never-default yes ipv6.method disabled
sudo nmcli --wait 15 connection up pwnbridge-usb
```

This does not provision Internet sharing. Preserve working host shared mode and device gateway/DNS if already configured. Adding new sharing/firewall policy is a separate administrator decision; do not overwrite Wi-Fi/default routes in order to get telemetry.

If **device** `usb0` is a `g_ether` gadget, already intended for NetworkManager, but marked unmanaged by a vendor udev rule, inspect `udevadm info /sys/class/net/usb0` and the vendor rules first. Only then add `/etc/udev/rules.d/90-pwnagotchi-usb-managed.rules` with this narrow match:

```udev
ACTION=="add|change|move", SUBSYSTEM=="net", ENV{DEVTYPE}=="gadget", ENV{INTERFACE}=="usb0", ENV{ID_NET_DRIVER}=="g_ether", ENV{NM_UNMANAGED}="0"
```

Back up any existing file. Reload rules with `sudo udevadm control --reload-rules`; schedule application to this specific device/reconnection while a recovery link is available. Do not replace the vendor rules file, make all interfaces managed, globally trigger devices, or add automatic `g_ether` resets. Carrier/address can remain present during a USB transfer stall; see the [README limitations](../README.en.md#verification-and-limitations).

## Optional existing PiSugar manager: Unix socket only

Pwnbridge itself only sends `get model`, `get battery`, `get battery_v`, `get battery_charging`, and `get battery_power_plugged` over `/tmp/pisugar-server.sock`. It does not need direct I²C access, charge/cutoff commands, or a Web UI. If the existing manager already provides a readable socket, **make no manager change**.

```sh
# Device / 本体; inspect the existing unit and executable before editing.
systemctl cat pisugar-server.service
command -v pisugar-server
pisugar-server --help
ss -lx | grep pisugar-server.sock
sudo ss -lntup
# Agent must be able to read the socket as pi, without sudo:
/usr/local/bin/pwnbridge-agent status
```

If the socket is absent, inspect the installed manager's supported arguments and existing power/button policy. Do not install another controller. For a compatible existing PiSugar 3 manager, a minimal UDS-only ExecStart looks like this (use `sudo systemctl edit pisugar-server.service` only after backing up/reviewing the current unit):

```ini
# Example drop-in only after confirming binary, model and existing config path.
[Service]
ExecStart=
ExecStart=/usr/bin/pisugar-server --model "PiSugar 3" --config /etc/pisugar-server/config.json --uds /tmp/pisugar-server.sock
```

Do not copy that example over a different model/path, discard necessary existing arguments, or add HTTP/WebSocket/TCP flags. Preserve the existing manager JSON and button/low-battery/shutdown behavior. A deliberate unit change needs `sudo systemctl daemon-reload` and an explicit manager restart; read back the effective unit and `ss` listeners afterward. Use the image/vendor's permission mechanism to let `pi` read the socket; do not chmod it world-writable as a workaround. No battery manager is required for CPU/UI features.

An empty/asleep PiSugar can be unreachable even while USB powers the Pi. Charge/wake it and recheck before blaming software or adding I²C/controller workarounds. Unknown readings stay null; `battery.available` only means percentage is known, not that every other battery field is valid.

## Rollback and remaining checks

Stop the host user service before restoring its old binary/unit/config. On the device, use the printed backup to restore only the files/settings you changed, and restart Pwnagotchi explicitly after review. If it was a first install, disable/remove only the Pwnbridge plugin entry/files, not the custom directory or other plugins. Disable the optional PAN timer before removing its helper; do not delete working USB/Wi-Fi profiles.

External prerequisites still needing your own verification include image/SDK compatibility, root service ownership, `pi` access, USB gadget stability, Bluetooth adapter support and local authorization policy. The known USB reboot stall is not fixed by this installation. See [verification and limitations](../README.en.md#verification-and-limitations) before treating reconnection as an availability guarantee.
