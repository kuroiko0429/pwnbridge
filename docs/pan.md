# Bluetooth PAN: 設定 / setup

[日本語の導入](install.ja.md) | [English installation](install.en.md)

## 方針 / Policy

PAN は任意の管理専用経路です。PC が NAP、本体が PANU。PC `pwnpan0` に `10.77.0.1/30`、本体に `10.77.0.2/30` を手動設定し、両方 `ipv4.never-default=yes`、gateway / DNS なし、IPv6 無効にします。**PAN は Internet を供給しません。** 既存の PC Wi-Fi / default route と USB Internet 共有は変更しません。

PAN is an optional management link: host NAP, device PANU, manually addressed as above. It supplies **no Internet gateway or DNS**. Preserve the host's Wi-Fi/default route and existing USB Internet sharing. Start with verified USB SSH; do not replace the working link to add PAN.

ここで使う MAC は自分の adapter から取得します。UUID は NetworkManager が作った実際の profile から取得し、公開の固定値を使いません。以下は Linux / BlueZ / NetworkManager を使う双方での手順です。別 manager を使う image へ、そのまま NetworkManager を重ねないでください。

Discover your own adapter addresses and actual profile UUIDs. These steps assume BlueZ and NetworkManager already manage the relevant networking on both Linux systems. Profile creation/pairing may require local polkit authorization or device sudo; do not repeatedly retry denied sudo authentication on the host.

## 1. Adapter と MAC の確認 / discover adapters

```sh
# Each machine separately: inspect the LOCAL controller.
# 双方で個別に: これはそのマシン自身の controller。
bluetoothctl list
bluetoothctl show
nmcli -f NAME,UUID,TYPE,DEVICE connection show
```

選んだ PC controller の address を `HOST_BT_MAC`、本体 controller の address を `ZERO_BT_MAC` に入れます。以下の文字列は placeholder なので置換必須。両 shell で同じ値を設定します。複数 controller がある場合は helper の `--adapter hci0` も実際のものに合わせてください。

Set both variables in **both shells** from the selected controllers, replacing the placeholders. Use the peer's address, not a nearby device name or an arbitrary scan result. Adapt `--adapter hci0` if you selected another local controller.

```sh
export HOST_BT_MAC='REPLACE_WITH_HOST_CONTROLLER_MAC'
export ZERO_BT_MAC='REPLACE_WITH_DEVICE_CONTROLLER_MAC'
```

## 2. 一時ペアリング / bounded pairing

`make build cross` で host / ARM64 helper を作ります。本体に一時コピーします（PC 側、repository root）。`IDENTITY` は USB 導入時と同じ秘密鍵の絶対パスです。

Build with `make build cross`, then copy the device helper from the repository root on the host. `IDENTITY` is the same absolute private-key path used for USB setup.

```sh
IDENTITY="$HOME/.ssh/id_ed25519"
scp -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  bin/pwnbridge-pan-pair.arm64 pi@10.12.194.1:pwnbridge-install/
```

**信頼範囲に注意:** `--trust-peer` は BlueZ `Device1.Trusted=true` を永続設定します。これは **端末全体の trust で、PAN 専用ではありません**。Trusted peer は Agent1 の service 許可確認を迂回できます。一時 agent の PAN UUID allowlist が、終了後も続く許可境界になるわけではありません。次の例は所有する 2 台の unattended reconnect のため、明示的にその広い trust を選択します。承認できない場合は**双方から flag を外し**、接続時の認可を別途設計してください。flag 省略は既存 trust を取り消す操作ではありません。

**Trust warning:** `--trust-peer` persists **DEVICE-WIDE BlueZ trust, not PAN-only trust**. Trusted peers can bypass Agent1 service authorization; the temporary PAN UUID allowlist is not a persistent restriction. The example explicitly opts into broad peer trust for unattended reconnection between your own devices. Omit the flag on **both** sides if you do not accept that policy, and arrange connection authorization separately. Omitting it leaves existing trust unchanged; it does not revoke it.

本体で passive helper を先に実行し、`PAIR WINDOW READY` を確認している間に PC で initiating helper を実行します。NoInputNoOutput / Just Works pairing は認証付き PIN 交換ではありません。telemetry の本人確認は USB で確認済みの SSH host key が担います。

Start the passive device helper over USB, wait for `PAIR WINDOW READY`, then start the host initiator during the same window. Just Works pairing is not an authenticated PIN exchange; the pinned SSH key remains the management identity check.

```sh
# Device / 本体: in the USB SSH shell; variables must be set here too.
sudo "$HOME/pwnbridge-install/pwnbridge-pan-pair.arm64" \
  --adapter hci0 --peer "$HOST_BT_MAC" --trust-peer --discoverable --timeout 90s
```

```sh
# Host / PC: a second terminal, repository root.
./bin/pwnbridge-pan-pair --adapter hci0 --peer "$ZERO_BT_MAC" \
  --trust-peer --initiate --timeout 70s
```

helper は exact peer の object path のみ許可し、legacy PIN / passkey request は拒否します。一時 agent は PAN service UUID のみ許可します。終了時は Discoverable / Pairable を元の値へ戻す処理を、それぞれ独立した 3 秒 timeout で試み、復元に失敗すれば pairing 成功後でもエラー終了します。永続 discoverable を必要とする daemon ではありません。正常終了と実際の flags を両側で確認してください。

The temporary agent allows only the exact peer and PAN service UUIDs, rejects legacy PIN/passkey requests, and attempts to restore the original Discoverable/Pairable flags on exit with independent 3-second timeouts. A restore failure is reported even if pairing succeeded. It is not a permanent discovery daemon. Verify both sides rather than assuming flags are now false: their original values are restored.

```sh
# Host
bluetoothctl show
bluetoothctl info "$ZERO_BT_MAC"
# Device
bluetoothctl show
bluetoothctl info "$HOST_BT_MAC"
```

## 3. Profile 作成 / create only missing profiles

まず既存 profile を列挙し、同名・同目的の profile があればその UUID で設定を確認します。以下の `add` は適切な設定が存在しないときだけ実行します。同名 profile が複数ある場合は、名前ではなく UUID で操作してください。

List profiles first. Inspect an existing candidate by UUID and reuse it if correct. Run each `add` command only when no suitable profile exists; duplicate names make name-based operations ambiguous.

```sh
# Host / PC: selected LOCAL adapter address for NAP.
nmcli connection add type bluetooth con-name pwnbridge-pan autoconnect yes \
  ifname pwnpan0 bluetooth.type nap bluetooth.bdaddr "$HOST_BT_MAC" \
  ipv4.method manual ipv4.addresses 10.77.0.1/30 \
  ipv4.never-default yes ipv4.gateway '' ipv4.dns '' \
  ipv6.method disabled bridge.stp no
nmcli --wait 15 connection up pwnbridge-pan
```

```sh
# Device / 本体: REMOTE host adapter address for PANU.
sudo nmcli connection add type bluetooth con-name 'Pwnbridge PAN' \
  bluetooth.type panu bluetooth.bdaddr "$HOST_BT_MAC" \
  connection.autoconnect yes connection.autoconnect-priority 100 \
  ipv4.method manual ipv4.addresses 10.77.0.2/30 \
  ipv4.never-default yes ipv4.gateway '' ipv4.dns '' ipv4.may-fail no \
  ipv6.method disabled
sudo nmcli --wait 20 connection up 'Pwnbridge PAN'
```

NAP 側は PAN client がつながるまで port 待ち・IP 未付与になる場合があります。host activation の timeout だけで全体を失敗と決めず、本体を接続して実際の bridge address と SSH を確認します。ただし `nmcli con up` の成功だけでも end-to-end の証明にはなりません。

The NAP bridge may wait for ports or lack its IP until the client connects. Inspect it after device activation; neither a host activation timeout alone nor a successful `nmcli con up` proves end-to-end behavior.

```sh
# Host / PC
ip -br address show pwnpan0
ip route show default
ping -c 3 10.77.0.2
ssh -F /dev/null -i "$IDENTITY" -o BatchMode=yes -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  pi@10.77.0.2 /usr/local/bin/pwnbridge-agent status
# Device / 本体
ip -br address
ip route show default
nmcli -f NAME,UUID,TYPE,DEVICE connection show --active
```

USB で信頼した **同じ** host key を使います。transport が変わったことを理由に未知鍵を承認したり、`StrictHostKeyChecking=no` を使ったりしないでください。Wi-Fi / USB の既存 address と Internet 用 route が変わっていないことも確認します。

Use the **same** pinned host key as USB. Never disable host-key checking just because the transport changed. Verify that existing Wi-Fi/USB addressing and Internet routes remain unchanged.

## 4. 本体の再接続 timer / device reconnect timer

`pwnbridge-pan` は既存 UUID の接続を一度だけ試す bounded helper です。既にその UUID が active なら何もしません。inactive なら設定 peer MAC と `bluetooth.type=panu` を検証してから NetworkManager に activation を依頼します。任意 profile の作成、shell、adapter 設定、USB / Wi-Fi 操作はありません。

The helper makes one bounded activation attempt for an existing profile UUID. An already active UUID is a no-op; an inactive one is checked for the configured peer and `panu` type before activation. It does not create profiles, execute arbitrary shell commands, change adapters, or touch USB/Wi-Fi.

まず PC から 3 ファイルをコピーします / copy these files from the host repository root:

```sh
scp -F /dev/null -i "$IDENTITY" -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=yes -o HostKeyAlias=10.12.194.1 \
  bin/pwnbridge-pan.arm64 packaging/device/pwnbridge-pan.service \
  packaging/device/pwnbridge-pan.timer pi@10.12.194.1:pwnbridge-install/
```

本体で実行します。先に一覧を見て、目的の profile が一つだけであることを確認します。重複があれば、下の自動取得を使わず一覧の正しい UUID を指定してください。既存 pan.env / unit / helper があれば、変更前にバックアップします。

On the device, confirm the profile is unambiguous before retrieving its UUID. If names are duplicated, select the intended UUID from the listing instead of using the name lookup below. Back up any existing helper, units and pan.env before replacing them.

```sh
# Device / 本体; HOST_BT_MAC must still contain the actual HOST adapter address.
nmcli -f NAME,UUID,TYPE,DEVICE connection show
PAN_UUID="$(nmcli -g connection.uuid connection show 'Pwnbridge PAN')"
# Inspect actual values: one UUID, the expected host MAC, and panu.
nmcli --escape no -g connection.uuid,bluetooth.bdaddr,bluetooth.type \
  connection show uuid "$PAN_UUID"
export PAN_UUID HOST_BT_MAC
sudo install -d -o root -g root -m 0700 /etc/pwnbridge
sudo env PAN_UUID="$PAN_UUID" HOST_BT_MAC="$HOST_BT_MAC" python3 - <<'PY'
import os
import re
from pathlib import Path
import shutil
import tempfile
import uuid
u = os.environ['PAN_UUID']
m = os.environ['HOST_BT_MAC']
if str(uuid.UUID(u)) != u.lower() or not re.fullmatch(r'(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}', m):
    raise SystemExit('Invalid profile UUID or peer MAC')
p = Path('/etc/pwnbridge/pan.env')
if p.exists():
    fd, backup = tempfile.mkstemp(prefix='pan.env.backup-', dir=str(p.parent))
    os.close(fd)
    shutil.copyfile(p, backup)
    print('Backup:', backup)
fd, tmp = tempfile.mkstemp(prefix='.pan-', dir=str(p.parent))
with os.fdopen(fd, 'w') as f:
    os.fchmod(f.fileno(), 0o600)
    f.write('PAN_PROFILE=' + u + '\nPAN_PEER=' + m + '\n')
os.replace(tmp, p)
PY
# Back up existing targets in a unique root-private directory before replacing.
PAN_BACKUP="$(sudo mktemp -d /var/backups/pwnbridge-pan-XXXXXXXX)"
for path in /usr/local/bin/pwnbridge-pan \
  /etc/systemd/system/pwnbridge-pan.service /etc/systemd/system/pwnbridge-pan.timer; do
  if sudo test -e "$path"; then
    sudo cp -a -- "$path" "$PAN_BACKUP/"
  fi
done
printf 'PAN file backup: %s\n' "$PAN_BACKUP"
sudo install -o root -g root -m 0755 "$HOME/pwnbridge-install/pwnbridge-pan.arm64" /usr/local/bin/pwnbridge-pan
sudo install -o root -g root -m 0644 "$HOME/pwnbridge-install/pwnbridge-pan.service" /etc/systemd/system/pwnbridge-pan.service
sudo install -o root -g root -m 0644 "$HOME/pwnbridge-install/pwnbridge-pan.timer" /etc/systemd/system/pwnbridge-pan.timer
sudo cat /etc/pwnbridge/pan.env
sudo systemctl daemon-reload
sudo systemctl enable --now pwnbridge-pan.timer
systemctl status pwnbridge-pan.timer
journalctl -u pwnbridge-pan.service -n 30 --no-pager
```

`pan.env` は root:root 0600。timer は起動約 20 秒後、その後 service 終了から約 15 秒ごとに試します。保証された復帰時間ではありません。設定は公開ログへ貼らず、peer と UUID が自分の profile に一致することを手元で確認してください。unit / helper 更新時に進行中の oneshot を強制再起動する必要はありません。

`pan.env` must be root:root 0600. The timer starts around 20 seconds after boot and retries around 15 seconds after the service becomes inactive; this is not a reconnection latency guarantee. Check the private configuration locally rather than publishing device identifiers. An in-flight oneshot need not be forcibly restarted during an update.

## 5. 確認・停止・制限 / checks, stop, limits

```sh
# Host / PC
pwnbridge status --json
ip route show default
# Device / 本体: inspect without disrupting links.
systemctl list-timers pwnbridge-pan.timer
sudo systemctl cat pwnbridge-pan.service
# Optional: stop unattended reconnect without removing profiles.
sudo systemctl disable --now pwnbridge-pan.timer
```

PC daemon は PAN 自体を作り直しません。OS/helper が復帰させた経路から認証済み frame が届いて初めて利用します。実機で切替・復帰の確認はありますが、空 image、全 adapter、物理 range、PC 再起動、既知 USB stall の恒久修正まで保証しません。動作中 USB を残して一方ずつ確認し、両経路断や自動 root reset を受け入れテストにしないでください。

The host daemon only redetects links brought back by the OS/helper, and trusts valid authenticated frames rather than link state alone. Known-hardware fallback/recovery checks do not guarantee blank-image installation, every adapter, physical range, host reboot behavior, or a permanent fix for the known USB stall. Keep USB recovery available and test one link at a time; do not add all-links-down tests or automatic privileged kernel resets.
