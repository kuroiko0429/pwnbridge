# ホスト daemon / CLI

Linux の一般ユーザーで動く Go 実装。Bluetooth PAN を優先し、USB の認証済みストリームを待機させる。PAN のペアリング、NetworkManager の設定、アダプター操作、root コマンドは一切実行しない。既存の PAN/USB の準備と target agent の配置は別作業。

## ビルド・起動

リポジトリのルートで実行する。実機側・SSH・ネットワークの前提は [インストール手順](install.ja.md) を参照。

```sh
make install-user
# 既存 config.json は保持される。初回生成された設定の鍵パス等を確認する。
export PATH="$HOME/.local/bin:$PATH"
pwnbridge daemon
```

手動 foreground daemon と user service は同時に起動しない。user service を利用する場合は以下の「user service」を参照。

別ターミナルから:

```sh
pwnbridge status
pwnbridge status --json
pwnbridge watch
pwnbridge watch --json
pwnbridge face --ttl 30s '(^_^)'
pwnbridge message --ttl 30s 'こんにちは'
pwnbridge clear
pwnbridge reconnect
```

フラグはサブコマンドの後、TEXT の前に置く。TEXT は一つの引数なので空白を含む場合は引用する。`pwnbridge face --ttl 30s -- '-_-'` のように、先頭ハイフン文字列には `--` を使う。TTL は整数秒の `1s`〜`300s`、既定 `30s`。小数秒の切り捨てはしない。顔は最大64 Unicode文字、メッセージは最大512文字。文字列は JSON stdin で送信し、SSH のコマンド文字列には含めない。

daemon はセッション D-Bus が必要。`status` は daemon の状態を表示し、切断状態も正常な状態レスポンスとして終了0で返す。RPC の validation/SSH/remote エラーは標準エラーと終了1で返す。Ctrl-C/SIGTERM で watch/daemon を停止できる。

## 設定スキーマ（JSON、未知のキーはエラー）

既定ファイルは `$XDG_CONFIG_HOME/pwnbridge/config.json`。XDG_CONFIG_HOME が未設定・空文字・相対パスなら `~/.config/pwnbridge/config.json` へフォールバックする。`pwnbridge daemon --config /absolute/path/config.json` で変更可能。

通常ファイルで、group/other に権限のない設定を要求する。推奨 `chmod 600 config.json`。JSON は一オブジェクトのみ、最大64KiB。`~` の展開は行わない。

```json
{
  "username": "pi",
  "identity_file": "/home/example/.ssh/id_ed25519",
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

| キー | 型 / 条件 | 省略時 |
|---|---|---|
| `username` | string、`[a-z_][a-z0-9_-]{0,31}` | 必須 |
| `identity_file` | string、絶対パス、改行/NUL不可 | 必須 |
| `host_key_alias` | string、厳密に `10.12.194.1` | 必須 |
| `remote_agent` | string、絶対パス、ASCII英数字と `_ . / -` のみ、正規化済み | 必須 |
| `endpoints` | object配列、1〜2要素、name重複不可 | 必須 |
| `endpoints[].name` | string、`bluetooth` または `usb` | 必須 |
| `endpoints[].address` | string、unicast IPv4、localhost/unspecified/multicast不可 | 必須 |
| `endpoints[].interface` | string、Linuxインターフェース名glob。空文字ならsubnetから導出 | `""` |
| `poll_interval` | duration string、100ms〜1m、remote stream の間隔 | `"1s"` |
| `stale_after` | duration string、poll_intervalより大きく5m以下 | `"5s"` |
| `retry_interval` | duration string、100ms〜1m、再試行backoffの初期値 | `"2s"` |
| `idle_timeout` | duration string、poll_intervalより大きく5m以下 | `"4s"` |
| `rpc_timeout` | duration string、1s〜1m | `"8s"` |
| `bluetooth_healthy_frames` | integer、1〜100、USBからBTへ戻る連続フレーム数 | `3` |

タイミングとhealthy_framesの `0` は省略と同じ既定値扱い。負の値は不正。identity_file の存在/秘密鍵の利用可能性は SSH が確認し、失敗は transport の診断に表示する。鍵自体の内容は読み出して記録しない。passphrase付き鍵は通常の ssh-agent 等で非対話認証可能にしておく。

USBのみで運用する場合は endpoints から Bluetooth 要素を削除する。USB の interface は既定では空文字。`"interface": ""` とすれば、UPなIPv4インターフェースでendpointを含む直接接続subnetを探す。

## 認証と通信

- 接続前に Linux のインターフェースの UP 状態、interface glob、ローカルアドレスのsubnet包含を検査。Wi-Fi のデフォルトルートだけでは接続を開始しない。SSH はそのローカル送信元アドレスとインターフェースに bind する。
- UP/subnet状態は実行中も500msごとに確認し、消失/変更ならworkerを無効化して停止する。bridgeがUPのまま対向だけ消えた場合はidle timeoutで検出する。
- 各endpointにつき永続SSH streamを一本。USB standbyも常時読み、毎秒SSHを起動する方式ではない。UI RPC時のみ、選択済みのfreshなendpointに別の短いSSHを一本起動する。
- 両経路を同じ既存known_hostsエントリー `10.12.194.1` に固定する。`StrictHostKeyChecking=yes`, `BatchMode=yes`, `IdentitiesOnly=yes`, `PasswordAuthentication=no`, `KbdInteractiveAuthentication=no`。未知鍵/異なる鍵は拒否する。
- `-F /dev/null` で個人ssh_config内のProxy/ControlMaster/LocalCommand等を使用しない。認証済みセッションの使い回し、port/agent/X11 forwardingはしない。known_hostsの自動更新、DNSによる鍵検証も無効。
- connect timeout 3s、SSH keepalive 2s × 2。NDJSON一行は64KiB未満、stderrは最大4096bytes保持。壊れたJSON、schema不一致、必須項目欠落、異常なsensor値はそのstreamを失敗扱いにする。
- EOF/invalid frame/idle timeoutで直ちにfresh資格を失う。プロセスとプロセスグループをcancel/killし、Waitで回収する。古いgenerationや失敗済みgenerationの遅延フレームでは復活しない。
- 再試行は設定初期値から指数backoff（通常は30sまで。初期値が30sを超える設定ならその初期値）。正常フレームのあった接続の失敗は初期値へ戻す。`reconnect` は待ち時間を解除し、両workerを再作成するだけで、BTの再ペアリング等はしない。
- RPCに任意shell、reboot、sudo等の入口はない。remote_agentは保守的に検証した固定パス、操作は `stream` と `rpc` のみ。RPC成功はremote JSON応答を確認してから返す。曖昧な失敗時に別経路で自動再送して二重適用しない。

## freshness / イベント

freshnessの基準は **ホストで正常フレームを受けた時刻**。targetの時計がずれていても選択は壊れない。BTがfreshなら優先し、USBからの復帰は既定で3連続正常フレームを待つ。USBが利用不能ならBTの最初のfreshフレームをすぐ使う。

`StateJSON` / `status --json` の外側の形:

```text
connected: bool
transport: "bluetooth" | "usb" | ""
stale: bool
last_seen: RFC3339Nano string | ""
last_error: string
snapshot: protocol.Snapshot | null
transports: { endpoint_name: { generation: uint64, fresh: bool, last_seen: string, error: string } }
```

両方失ったら `connected=false, transport="", stale=true`。最後のsnapshot/last_seenは残すが現在値ではない。受信前はsnapshot自体がnull。sensorのnullを0度/0%に変換しない。CPU/battery/UIの個別エラーはsnapshotに保持し、human statusでも表示する。待機経路だけの障害はtransports内のerrorで確認する。

Event JSONは `type`, `at`（ホスト時刻）、`transport`, 任意の `previous_transport`, `boot_id`。

- `connected`: 起動後最初の正常接続。
- `disconnected`: freshな経路が一つもなくなったときだけ。standbyがfreshなら出さない。
- `recovered`: 一度接続した後、全経路断から再び接続。
- `transport_changed`: 接続を維持したまま経路が変わった。
- `device_restarted`: BootIDの変化。既知の古いBootIDをstandbyが遅れて送っても旧bootに巻き戻さない。

`reconnect`の成功は再試行要求を受理したことだけを意味し、接続完了ではない。status/watchでrecoveredを確認する。全workerを意図的に無効化するため、この手動操作ではdisconnected/recoveredが発生し得る。

`watch --json` はNDJSON。初期値とPropertiesChangedは `{"type":"state","state":{...}}`、EventはそのJSONを直接一行で表示する。daemon終了/セッションbus消失はエラー終了し、busy-loopしない。daemon再起動時はwatchも起動し直す。イベント履歴の永続保存はしない。

## セッション D-Bus

daemon はローカル Unix socket の session bus（`DBUS_SESSION_BUS_ADDRESS` の `unix:path=` / `unix:abstract=`）へ接続する。未設定なら `$XDG_RUNTIME_DIR/bus`（XDG_RUNTIME_DIR も未設定なら `/run/user/<uid>/bus`）を使う。TCP bus / autolaunch は使わない。認証・Hello・名前取得には5秒の起動時deadlineを設ける。

通知はruntime mutex内で最大64バッチのFIFOへnonblocking enqueueし、socketへの書き込みはmutex外のpublisherが行う。同じバッチではPropertiesChangedの後にEventを元の順序で送る。queue overflow、送信エラー、1バッチの送信が2秒以内に完了しない場合は、イベントを黙って捨てて運転を継続せずbusを閉じ、daemonをエラー終了してuser serviceの再起動対象にする。Ctrl-C/SIGTERMでもraw socketを先に閉じて送信を中断し、その後godbus接続を閉じる。切断時に未送信の通知を永続化・再送する保証はない。

- service/interface: `io.github.kuroiko.Pwnbridge1`
- object: `/io/github/kuroiko/Pwnbridge1`
- methods: `GetState() -> s`, `SetFace(s,u)`, `SetMessage(s,u)`, `ClearOverride()`, `Reconnect()`
- signals: 同interfaceの `Event(s)`、標準interfaceの `org.freedesktop.DBus.Properties.PropertiesChanged(s,a{sv},as)`。
- introspection対応。全プロパティはread-only。PropertiesChangedは更新後のプロパティ辞書全体を送り、invalidated配列は空。

| property | D-Bus型 | 意味 |
|---|---|---|
| Connected | b | freshな認証済みtelemetryがある |
| Transport | s | bluetooth / usb / 空文字 |
| Face / Message / Mode | s | 最後の選択済みsnapshot値 |
| CPUTemperature | d | 数値。下記Validがfalseなら利用しない |
| CPUTemperatureValid | b | 接続中かつCPU値が既知 |
| BatteryPercent | d | 数値。下記Availableがfalseなら利用しない |
| BatteryAvailable | b | 接続中かつbattery.available=trueかつpercentが既知 |
| StateJSON | s | nullを保持した完全な状態とstale情報 |
| LastError | s | 全経路断/直近RPCのエラー。個別sensor/standby診断はStateJSON参照 |
| LastSeen | s | 最後の選択済みsnapshotのホスト受信時刻。受信前は空 |

D-Busのdouble型はnullを表せないため、未知時のdoubleは0という**無効なplaceholder**で、必ずValid/Availableと併用する。JSON側の未知sensorはnullのまま。切断中に最後の数値が残ってもValid/Availableはfalse。

SetFace/SetMessageのTTL引数はuint32秒。入力エラーは `.Error.InvalidArgs`、remote/接続エラーは `.Error.Failed`。read-only propertyへの書き込みは標準のPropertyReadOnlyエラー。長いRPCでもconfigのrpc_timeoutで打ち切る（CLIのbus呼び出し上限は65s）。

## user service（利用者が必要に応じて実行）

```sh
make install-user
systemctl --user enable --now pwnbridge.service
systemctl --user status pwnbridge.service
journalctl --user -u pwnbridge.service -f
```

ExecStartは `%h/.local/bin/pwnbridge daemon`。`Type=dbus` と `BusName` によりAPI登録まで起動完了を待つ。`Restart=always` で予期しない終了・バス名喪失後は3sで再起動する（明示的な `systemctl --user stop` は再起動しない）。Type=dbusではバス名消失によりsystemdがSIGTERMを送るため、正常なsignal終了扱いでも再起動が必要なケースを含める。秘密鍵、known_hosts、PATH上のOpenSSH、target agentの配置は利用者が管理する。サービス有効化・root操作・PAN設定変更はこのプログラム自身では行わない。

## テストと制限

```sh
go test -race ./internal/host ./cmd/pwnbridge
dbus-run-session -- go test -race ./internal/host ./cmd/pwnbridge
go vet ./internal/host ./cmd/pwnbridge
go build ./cmd/pwnbridge
```

選択engineは外部から時刻を供給する決定論的テスト。stream/RPCテストは実際のhelper子プロセスで行い、EOF、invalid schema、過大frame、無応答timeout、cancel/Wait、両常設worker、BT→USB→BT、全断→復帰を検証する。D-Busテストは各々privateな `dbus-run-session` を起動するため、実デスクトップbusのdaemon名を奪わない。dbus-run-sessionがない環境のみそのテストをskipする。テスト用データやコマンド差し替えはテストコード内に限定し、通常サービスに生成telemetryは含めない。

Linux/OpenSSH前提。PANそのものを接続し直すのは別のOS設定/helperの責務で、daemonは利用可能になった経路を再検出する。インターフェースUPとsubnetは到達性/鍵認証の代わりにならず、実際のSSHフレームで初めてConnectedになる。疎通未確認のリンクを成功扱いにしない。実機での確認にはtarget `/usr/local/bin/pwnbridge-agent` とUI pluginの配置が必要。I2C未接続ならbatteryのerror/nullは仕様どおりで、host側で読み値を合成しない。
