# Pwnbridge

**日本語** | [English](README.en.md)

Pwnagotchi の状態を Linux PC から取得し、顔やセリフを一時変更できる常駐デーモン＋CLIです。Bluetooth PAN を優先し、認証済みの USB ストリームを代替経路として待機させます。GUI ウィジェットではなく、デスクトップ連携用のセッション D-Bus API を提供します。

## できること

- 本体の顔・セリフ・モード、CPU 温度、稼働時間、起動識別子を取得。
- 既存の PiSugar manager から残量・電圧・充電中・外部給電状態を読み取り専用で取得。不明なセンサー値は `null` とエラーのまま返し、0 や前回値で埋めません。
- 顔とセリフをそれぞれ 1〜300 秒の期限付きで変更し、明示的に解除。
- Bluetooth → USB → Bluetooth の切替、全断からの復帰、本体再起動を検出。切断中の最終値には stale を付けます。
- CLI と D-Bus を使って、お好みのデスクトップ UI と連携。

```text
Linux PC: Go daemon + CLI + session D-Bus
  ├─ SSH / Bluetooth PAN → 本体 10.77.0.2      優先
  └─ SSH / USB           → 本体 10.12.194.1   待機・代替
                                 │
                        Go pwnbridge-agent (pi)
                          ├─ CPU / 稼働時間 / 起動識別子
                          ├─ 既存 PiSugar Unix socket（読取専用）
                          └─ Unix socket → 最小 Python UI plugin
```

PC デーモン、本体 agent、PAN 再接続 helper は Go 製。Python は Pwnagotchi 自身の UI へ接続する部分だけです。新しい HTTP/TCP サーバー、Web UI、攻撃操作、電源操作、firmware 変更、自動 kernel reset は追加しません。デーモン自体はネットワーク設定やペアリングを行いません。

## 必要なもの

- PC: Linux、Go **1.24 以降**、GNU Make、installer / ローカルテスト用 Python **3.13 以降**、OpenSSH、systemd user service、**Unix socket のセッション D-Bus**。private bus のテストには `dbus-run-session` が必要です。
- 本体: ARM64 の Pwnagotchi、既存の `pi` ユーザー／グループ、SSH 鍵認証、root で動く Pwnagotchi と互換 UI SDK。導入手順では `/opt/.pwn/bin/python` と、設定編集用の `tomlkit` を使用します。
- PAN を使う場合: 双方に BlueZ / NetworkManager、利用可能な Bluetooth adapter、ペアリングと接続設定の権限。
- バッテリー取得は任意: 既存 PiSugar manager の `/tmp/pisugar-server.sock`。電源管理サービスをもう一つ導入するツールではありません。

実機確認した構成は Raspberry Pi Zero 2 W、Waveshare 2.13-inch V3、PiSugar 3 です。別 image・architecture・display・battery は個別に確認が必要です。空の SD カードから環境を作る installer ではありません。

## ビルド・導入

Linux PC で実行します。

```sh
git clone https://github.com/kuroiko0429/pwnbridge.git
cd pwnbridge
make build cross
make test check
make install-user
export PATH="$HOME/.local/bin:$PATH"
```

`make cross` は本体向け ARM64 binary を作ります。クロスビルド成功は実機動作の証明ではありません。`make install-user` は PC の binary / user unit を配置し、設定がない場合だけ非公開権限の初期設定を生成します。sudo やサービスの有効化・再起動は行いません。既存設定は保持するので、起動前に秘密鍵の絶対パスを確認してください。

**続けて [日本語の導入手順](docs/install.ja.md) を実施してください。** USB の SSH host key 確認、本体の 3 ファイル配置、既存 TOML を残した plugin 有効化のあと、必要なら PAN を追加します。[English installation guide](docs/install.en.md) もあります。

```sh
# 本体の準備・設定確認・host key 固定の SSH 確認が済んでから:
systemctl --user enable --now pwnbridge.service
pwnbridge status --json
```

## 使い方

```sh
pwnbridge status
pwnbridge status --json
pwnbridge watch
pwnbridge watch --json
pwnbridge face --ttl 15s '(^_^)'
pwnbridge message --ttl 20s 'こんにちは'
pwnbridge face --ttl 15s -- '-_-'
pwnbridge clear
pwnbridge reconnect
```

フラグは TEXT の前に置きます。TTL は整数秒、既定 `30s`。顔は 64、セリフは 512 Unicode 文字までです。`reconnect` は通信 worker の再試行だけを促し、再ペアリングや経路設定は行いません。`status` の終了コードが 0 でも、接続済みとは限らないので `connected` を確認します。

TTL / clear / unload の復元先は、**最後に観測できた native 値への best-effort**です。同時更新された最新値の原子的な保存・復元や、e-paper の即時描画は保証しません。UI 適用やファイル公開の途中で失敗すると `UI request outcome indeterminate; restoration attempted` が返る場合があります。「未適用」と決めつけず、状態を確認し、別経路への自動再送は避けてください。

## セッション D-Bus API

```text
Bus:        user/session（Unix socket のみ。system bus ではない）
Service:    io.github.kuroiko.Pwnbridge1
Object:     /io/github/kuroiko/Pwnbridge1
Interface:  io.github.kuroiko.Pwnbridge1
```

| Method | 引数 → 戻り値 |
|---|---|
| `GetState` | なし → JSON を含む string |
| `SetFace` | string, uint32 TTL 秒 → なし |
| `SetMessage` | string, uint32 TTL 秒 → なし |
| `ClearOverride` | なし → なし |
| `Reconnect` | なし → なし |

読取専用 properties: `Connected`, `Transport`, `Face`, `Message`, `Mode`, `CPUTemperature`, `CPUTemperatureValid`, `BatteryPercent`, `BatteryAvailable`, `StateJSON`, `LastError`, `LastSeen`。

温度・残量の数値は必ず Valid / Available と併用してください。D-Bus double は JSON の `null` を表せません。`StateJSON` には null、個別センサーエラー、snapshot の freshness、各経路の診断が残ります。切断中の最終値は現在値ではありません。

Signals は標準 `org.freedesktop.DBus.Properties.PropertiesChanged` と `Event(string JSON)`。event type は `connected`, `disconnected`, `recovered`, `transport_changed`, `device_restarted` です。通知は最大 64 バッチの FIFO、1 バッチの公開期限は 2 秒。overflow / 送信失敗時は黙って欠落させて運転を続けず、bus を閉じて異常終了し、user service の再起動対象になります。永続イベントログではありません。daemon が消失したら `watch` も起動し直してください。

```sh
busctl --user introspect io.github.kuroiko.Pwnbridge1 /io/github/kuroiko/Pwnbridge1
busctl --user call io.github.kuroiko.Pwnbridge1 /io/github/kuroiko/Pwnbridge1 io.github.kuroiko.Pwnbridge1 GetState
```

詳細: [host API・設定](docs/host.md)、[本体側の仕組み](device/README.ja.md)、[PAN の設定と信頼範囲](docs/pan.md)（日英）。

## 確認範囲と制限

ローカルテストは入力検証、部分的な telemetry 障害、実 subprocess の終了・backpressure、private D-Bus、テスト専用 SDK fixture を使った Python Unix socket bridge を対象にしています。既知の実機構成では USB / PAN の鍵認証 telemetry、表示の TTL / clear、切替・復帰、起動識別子の変化を確認しています。**空の SD カードへの新規導入、全 image / SDK、物理的な抜き差し・無線到達範囲、PC 再起動の全条件を検証したものではありません。**

PiSugar は充電・wake 後に実測値の取得が復帰しています。電池が空、または MCU が休止していると、Pi 本体が USB 給電で動いていても I²C が応答しない場合があります。読めなければ null とエラーを返し、充電・電源カット・controller 設定は変更しません。

本体再起動後、carrier / IP が正常でも USB 転送が止まる事象が再発しています。動作中の Bluetooth 経由で本体の `g_ether` を手動 reset して復旧した例はありますが、**恒久対策ではなく、自動実行もしません**。USB 列挙、interface / address、TCP / SSH、認証済みフレームを段階的に確認してください。carrier だけで代替経路を正常扱いしたり、確認のため両経路を一度に切ったりしないでください。

## ローカルテストについて

`make test check` で Go の race test / vet と Python テストを実行します。導入手順の TOML 編集を検証する追加テストには `tomlkit` が必要です。未導入ならそのテストだけ skip され、CI では `tomlkit` を導入して実行します。本体 SDK・root 権限・実機接続はローカルテストには不要です。

## ライセンス

GPL-3.0。[LICENSE](LICENSE) を参照してください。
