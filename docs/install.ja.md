# 導入手順（Linux PC + ARM64 Pwnagotchi）

[English](install.en.md) | [日本語 README](../README.md)

これは利用者向けの導入手順で、空の image への fresh install を検証済みという意味ではありません。既存の `pi` ユーザー／グループ、USB gadget 接続、承認済みの本体 sudo がある、動作中の Pwnagotchi image を前提にします。image の違いを埋めるため、新しい管理者ユーザーを安易に作らないでください。SD カード・設定をバックアップし、USB の復旧経路を残して進めます。操作するのは所有または許可された端末・ネットワークだけにしてください。

PC と本体のコマンドを分けています。この文書を作るだけで既存サービスへ変更が加わることはありません。ネットワーク変更やサービス再起動は各手順を読んでから明示的に行います。ファイル配置だけなら、本体の reboot は不要です。

## 1. PC でビルド

Go 1.24 以降、GNU Make、Python 3.13 以降、OpenSSH、Git、systemd、D-Bus tools をディストリビューションの方法で用意します。`make test` は race detector を使うので、対応する native Go toolchain / C compiler が必要な場合があります。PAN を使うなら双方に BlueZ / NetworkManager も必要です。普段のデスクトップユーザーで実行します。

```sh
git clone https://github.com/kuroiko0429/pwnbridge.git
cd pwnbridge
make build cross
make test check
make install-user
export PATH="$HOME/.local/bin:$PATH"
```

PC installer は sudo を使わず、既存設定を保持し、サービスの起動・再起動もしません。初回設定には HOME から導出した秘密鍵の絶対パスを入れ、0600 で作成します。ログイン時も `$HOME/.local/bin` を PATH に含めてください。本体用成果物は `bin/pwnbridge-agent.arm64`、`bin/pwnbridge-pan.arm64`、`bin/pwnbridge-pan-pair.arm64`。binary release の取得は不要です。

## 2. USB 接続と SSH host key を確認

既定は本体 `10.12.194.1/28`、PC `10.12.194.2/28`。給電専用端子ではなく、data 対応 USB port / cable を使います。既存の接続設定と Internet 共有は維持してください。アドレスがなければ [後述の USB 設定](#usb--networkmanager-の補足) を確認します。

信頼できる本体 console、または認証済みの別経路で fingerprint を確認します。

```sh
# 本体の信頼できる console:
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

PC の通常ユーザーの known_hosts へ、同じ fingerprint の鍵だけを登録します。`ssh-keyscan` の結果だけを本人確認に使ったり、鍵が変わったからと旧 entry を無条件に削除したりしないでください。既存の SSH 鍵を使い、未登録なら image の信頼できる初期設定手段で公開鍵を登録します。秘密鍵は本体にコピーしません。passphrase 付き鍵は通常の ssh-agent に読み込み、user service からも agent を利用できるようにします。

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

daemon は個人の ssh_config を読みません（`-F /dev/null`）。password fallback は無効で、通常の known_hosts を使います。手動確認も同じ条件に揃えてください。USB / PAN の両方で `HostKeyAlias=10.12.194.1` を使います。現行 validator はこの値を厳密に要求します。

## 3. 本体の基本 3 ファイルを配置

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

同じ鍵固定のオプションで USB SSH shell を開き、以下を本体で実行します。設定と置き換える既存ファイルは、root だけが読める一意な `/var/backups/pwnbridge-*` にバックアップします。既存の `main.custom_plugins` を読み、そのディレクトリを使い、他の TOML 設定・コメントは保持します。有効化するのは `main.plugins.pwnbridge.enabled` だけで、他 plugin / radio の flags は変更しません。filesystem root から custom directory までの全 path component は、symlink ではない実ディレクトリで、root 所有かつ group / other 書込不可が必要です。安全でない path は手動で調査・修正し、installer は再帰 chown しません。backup・ファイル配置より前に編集後 TOML を再 parse し、意図した document と設定が違えば中止します。無関係なディレクトリを再帰 chown するのではなく、既存 path を先に確認してください。

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

配置するのは agent、plugin、tmpfiles のみです。agent の独立 system service は不要で、`pi` の SSH session 内で起動します。Python worker は Pwnagotchi が所有します。再起動前に実際の Python SDK、更新後 TOML、表示された backup との差分を確認してください。

```sh
# 本体: 設定は手元だけで確認し、そのまま公開しない。
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
# USB の復旧経路を確認してから明示的に再起動:
sudo systemctl restart pwnagotchi.service
sudo journalctl -u pwnagotchi.service -n 40 --no-pager
stat -c '%U %G %a %n' /run/pwnbridge /run/pwnbridge/control.sock /run/pwnbridge/ui.json
/usr/local/bin/pwnbridge-agent status
```

期待する権限は `/run/pwnbridge` が root:root **0755**、`control.sock` が root:pi **0660**、`ui.json` が root 所有 **0644**。socket を 0666 に緩めないでください。プロセスが起動しただけで live UI が使えるとは限りません。`ui_available` / `ui_error` と snapshot を確認します。既存で無効な Web UI・reporting・updates・能動的 radio 操作はそのままにし、この plugin のために有効化しないでください。

## 4. PC の非公開設定を確認

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

`XDG_CONFIG_HOME` は絶対 path の場合だけ使用し、未設定・空・相対 path なら、確認・権限設定とも `$HOME/.config` を使います。表示されたファイルをエディターで編集します。既存の動作設定を、例に合わせるためだけに丸ごと置き換えないでください。`identity_file` は実在する秘密鍵の**絶対パス**です。JSON の `~` / `$HOME` は展開されません。下記の placeholder はそのままでは使えません。root:pi socket に合わせて `username=pi`、host key alias は固定値を維持します。

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

USB の `interface` が空なら、UP かつ直接接続 subnet に属する interface を探します。PC 固有の名前を持ち込まずに済みますが、subnet が重複する場合は実際の interface を指定してください。USB だけなら Bluetooth endpoint を削除します。IP / carrier だけでは正常扱いせず、認証済み SSH telemetry の受信が必要です。

```sh
# PC: XDG_CONFIG_HOME を変更している場合は、表示された path を使う。
python3 - <<'PY'
import os
from pathlib import Path
xdg = Path(os.environ.get('XDG_CONFIG_HOME') or '.')
base = xdg if xdg.is_absolute() else Path.home() / '.config'
(base / 'pwnbridge/config.json').chmod(0o600)
PY
```

## 5. 任意: Bluetooth PAN

日英併記の [PAN 手順](pan.md) に進みます。MAC の確認、端末全体の trust を明示選択する一時 pairing、PC NAP `10.77.0.1/30` / 本体 PANU `10.77.0.2/30` の手動 address、**実際に取得した UUID** を使う再接続 timer を説明しています。

接続後、同じ本体 host key で認証できることを PC から確認します。

```sh
ssh -F /dev/null -i "$IDENTITY" -o BatchMode=yes -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  pi@10.77.0.2 /usr/local/bin/pwnbridge-agent status
```

PAN は管理専用で Internet gateway / DNS は提供しません。PC の Wi-Fi / default route と、既存 USB Internet 共有は維持します。

## 6. PC サービスを起動・確認

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

JSON の `connected` / `transport` / `stale` と各経路のエラーを見ます。battery の障害と CPU / UI は独立です。更新前からサービスが動いていた場合、`enable --now` だけでは process が置き換わらないことがあります。新 binary / 設定を反映すると決めたときだけ明示的に restart してください。操作はデスクトップの通常ユーザーで行い、`sudo systemctl` は使いません。

```sh
systemctl --user restart pwnbridge.service   # 更新・設定変更を明示的に反映
systemctl --user stop pwnbridge.service
```

停止しても network profile は消さず、本体も shutdown しません。動作確認のため両経路を一度に切らず、別の復旧経路を確認してから一方だけを試してください。物理 cable / radio の確認はローカルテストとは別です。

## USB / NetworkManager の補足

nmcli は、その interface を実際に NetworkManager が管理している場合だけ使います。image の既存 network manager に重ねて別 manager を有効化しないでください。追加前に双方で既存 profile を確認します。

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

`USB_PROFILE_UUID` には一覧で確認した UUID を設定します。公開の固定値ではありません。address / gateway / DNS / `ipv4.method` を確認し、編集前に対応する connection file をバックアップしてください。system connection file には秘密情報が含まれる場合があります。既存の shared profile を manual に変えないでください。以下は適切な profile が**存在しない場合だけ**作る管理専用の例です。

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

この例は Internet 共有を新設しません。既存の PC shared mode、本体の gateway / DNS は維持してください。共有や firewall policy の追加は別途管理者が判断する作業です。telemetry のために Wi-Fi / default route を上書きしません。

**本体** `usb0` が `g_ether` gadget で、NetworkManager 管理を意図しているのに vendor udev rule で unmanaged になる場合だけ、`udevadm info /sys/class/net/usb0` と vendor rule を確認します。該当する場合の `/etc/udev/rules.d/90-pwnagotchi-usb-managed.rules` は次の狭い match です。

```udev
ACTION=="add|change|move", SUBSYSTEM=="net", ENV{DEVTYPE}=="gadget", ENV{INTERFACE}=="usb0", ENV{ID_NET_DRIVER}=="g_ether", ENV{NM_UNMANAGED}="0"
```

既存ファイルがあれば先にバックアップします。`sudo udevadm control --reload-rules` で再読込し、この device の再接続・反映は復旧経路があるときに計画してください。vendor rule の丸ごと置換、全 interface の強制 managed、全 device の一斉 trigger、自動 `g_ether` reset は行いません。carrier / address が残ったまま転送停止する場合もあります。[README の制限](../README.md#確認範囲と制限) も確認してください。

## 任意: 既存 PiSugar manager の UDS のみを利用

Pwnbridge が `/tmp/pisugar-server.sock` に送るのは `get model` / `get battery` / `get battery_v` / `get battery_charging` / `get battery_power_plugged` だけです。直接 I²C、充電・cutoff 操作、Web UI は不要です。既存 manager の socket を読めるなら、**manager は変更しません**。

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

socket がなければ、導入済み manager の引数と、既存の電源・button policy を確認します。別 controller は追加しません。互換の既存 PiSugar 3 manager での最小 UDS-only ExecStart の例は以下です。unit のバックアップ・内容確認後に限り `sudo systemctl edit pisugar-server.service` で調整してください。

```ini
# Example drop-in only after confirming binary, model and existing config path.
[Service]
ExecStart=
ExecStart=/usr/bin/pisugar-server --model "PiSugar 3" --config /etc/pisugar-server/config.json --uds /tmp/pisugar-server.sock
```

異なる model / path へそのまま適用したり、必要な既存引数を捨てたり、HTTP / WebSocket / TCP の flag を加えたりしないでください。既存 manager JSON と button / low-battery / shutdown 動作は保持します。unit を意図的に変えた場合だけ `sudo systemctl daemon-reload` と manager の明示 restart を行い、実効 unit と `ss` の listener を再確認します。`pi` の読取権限は image / vendor の仕組みで設定し、world-writable chmod で回避しないでください。CPU / UI だけなら battery manager は不要です。

PiSugar の電池が空、または休止していると、Pi が USB 給電で動いていても通信できないことがあります。充電・wake 後に再確認し、すぐ I²C / controller workaround を足さないでください。不明値は null のままです。`battery.available` は残量が既知という意味で、他の battery field まで有効とは限りません。

## 巻き戻しと、各環境で必要な確認

PC は user service を停止してから旧 binary / unit / config を戻します。本体は表示された backup から変更したファイル・設定だけを戻し、確認後に Pwnagotchi を明示 restart します。初回導入を戻すなら Pwnbridge の entry / files だけを無効化・削除し、custom directory や他 plugin は消しません。任意 PAN helper を削除する前に timer を止め、動作中 USB / Wi-Fi profile は残してください。

image / SDK 互換性、root service、`pi` の権限、USB gadget の安定性、Bluetooth adapter / policy は利用環境ごとの確認が必要です。既知の USB reboot stall はこの導入では修正しません。[確認範囲と制限](../README.md#確認範囲と制限) を読み、再接続を可用性保証と扱わないでください。
