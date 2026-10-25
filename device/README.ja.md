# Pwnbridge: 本体側の仕組み

[日本語の導入手順](../docs/install.ja.md) | [English installation](../docs/install.en.md) | [README](../README.md)

本体側は Go の `pwnbridge-agent` と、Pwnagotchi UI に接続する最小の Python plugin です。再現可能な配置・backup・TOML 編集手順は上記の導入ガイドにまとめています。これは開発担当の作業メモではなく、利用者向けの仕様と運用上の境界です。

## 構成と配置

| ファイル | 配置先 / 役割 |
|---|---|
| `bin/pwnbridge-agent.arm64` | `/usr/local/bin/pwnbridge-agent`、root:root 0755。SSH の `pi` として実行 |
| `device/pwnbridge.py` | 既存 `main.custom_plugins` 配下。未設定時だけ `/usr/local/share/pwnagotchi/custom-plugins` を利用。root 所有 0644 |
| `device/pwnbridge.tmpfiles.conf` | `/etc/tmpfiles.d/pwnbridge.conf`、root:root 0644 |
| `/run/pwnbridge` | tmpfiles が root:root **0755** で作成 |
| `/run/pwnbridge/control.sock` | root 実行の plugin が作成。root:pi **0660**、Unix socket のみ |
| `/run/pwnbridge/ui.json` | root 所有 0644。一時ファイルから atomic replace で公開 |

agent 単体の常駐 system service は不要です。host が SSH で `stream` / `rpc` を起動し、plugin worker は Pwnagotchi の lifecycle に従います。plugin は root 所有の安全な runtime directory を要求します。socket を 0666 に変更したり、代わりに TCP listener を開いたりしないでください。既存の `pi` アカウント／グループがある image を前提とします。

## agent のコマンド

以下は本体の **pi ユーザー**で実行します。agent 自体に sudo や直接 I²C access は不要です。

```sh
/usr/local/bin/pwnbridge-agent status
/usr/local/bin/pwnbridge-agent stream --interval 1s
# stream は Ctrl-C で停止してから、次を実行。
printf '%s\n' '{"op":"set_face","face":"(^_^)","ttl_seconds":5}' | /usr/local/bin/pwnbridge-agent rpc
printf '%s\n' '{"op":"set_message","message":"こんにちは","ttl_seconds":5}' | /usr/local/bin/pwnbridge-agent rpc
printf '%s\n' '{"op":"clear_override"}' | /usr/local/bin/pwnbridge-agent rpc
```

`status` は JSON、`stream` は NDJSON、`rpc` は stdin の JSON request 一つを受けます。shell / 任意 path / power / reboot / firmware / radio 操作の API はありません。設定内容、秘密鍵、他の UI field を返す機能もありません。

## 読み取りと unknown の扱い

- CPU 温度は `/sys/class/thermal/thermal_zone0/temp`、稼働時間は `/proc/uptime`、起動識別子は `/proc/sys/kernel/random/boot_id`。
- UI は固定の `/run/pwnbridge/ui.json`。読込後の時刻に対して 15 秒より古い snapshot、未来 timestamp、不正 schema は unavailable にします。CPU / UI / battery は独立して失敗できます。
- PiSugar は既存 `/tmp/pisugar-server.sock` へ `get model`, `get battery`, `get battery_v`, `get battery_charging`, `get battery_power_plugged` **のみ**。各 query は別 connection、150 ms deadline、応答上限 1024 bytes。書き込み command は送りません。
- バッテリーの読取不能・parse error・範囲外は該当値を `null` と error にします。`battery.available=true` は有効な percentage が取得できた意味で、voltage / charging / external power がすべて既知とは限りません。前回値や 0% で正常に見せません。
- 既存 manager の UDS だけを利用し、新しい Web UI や電源 controller は不要です。電池が空・休止中なら、Pi が USB 給電で動いていても PiSugar の I²C が応答しないことがあります。充電・wake 後に復帰した実例がありますが、取得不可の全原因を断定するものではありません。

PiSugar の `/tmp` socket について、root peer の認証と symlink 拒否は追加していません。local socket を用意する既存 manager と本体のローカル権限境界を信頼する設計です。悪意ある同居ユーザーからのセンサー値偽装まで防ぐものではありません。

## UI bridge と安全境界

plugin は SDK の `ui.get('face'/'status'/'mode')` / `ui.set()` を使い、`ui.update()` を再帰呼出ししません。UI callback 内で socket / file I/O をせず、最新 UI reference 一つの bounded queue と worker で処理します。既存 listener や SDK の private lock は置き換えません。

face / message の期限は独立し、monotonic clock を使用します。TTL / clear / unload の復元は **最後に観測できた native 値への best-effort**。SDK の get / set は別々の lock なので、間に入った native write や override と同値の write を完全には識別できず、最新値の原子的な保存・復元は保証しません。描画は既存 UI の通常更新周期に従います。

UI 変更とファイル公開が成功してから ack しますが、両者を一つの atomic transaction にはできません。適用・公開の途中で失敗した場合は以前の override / deadline / UI を best-effort で戻し、`ok:false` と `UI request outcome indeterminate; restoration attempted` を返します。「不正入力だから未適用」と誤認せず、状態を確認してから対処してください。期限を延長した自動再試行や、別経路への自動再送は行いません。

## 入力・待ち時間の上限

| 項目 | 上限 |
|---|---|
| face / message | 64 / 512 Unicode 文字 |
| TTL | 整数 1〜300 秒 |
| plugin request | 8192 bytes |
| agent stdin / response frame | 16384 bytes |
| socket client | 同時処理 1、backlog 4 |
| plugin request / reply deadline | 全体 250 ms / 100 ms |
| agent stdin / socket RPC / stdout | 2 秒 / 1 秒 / 独立した 1 秒 |

unknown / duplicate / null field、大小文字 alias、不正 Unicode surrogate、追加 JSON、未知 operation を拒否します。空文字による一時的な非表示は可能です。slow client の断続送信で deadline は延長しません。

symlink、不正な control path、安全でない parent、別の active listener を拒否します。root 所有の stale socket は connection-refused を確認してから回収します。startup / unload を直列化し、unload 後の遅延 callback で worker を再起動しません。終了時は override を復元し、この instance 自身の socket / state を削除します。

agent の RPC stdout cleanup は cancellation 時に非同期です。通常 executable は blocked output でも終了できるようにしていますが、独自の終了しない Writer / Closer を渡す library 利用では goroutine が残る場合があります。

## ビルド・ローカル確認と実機の区別

PC の repository root で:

```sh
make build cross
make test check
```

Python 3.13 互換を想定したコードです。ローカル Python の文法確認や fixture を使った成功は、対象 image の `/opt/.pwn/bin/python` と実 SDK での動作保証ではありません。導入時に実 interpreter の import、設定反映、socket の所有権、`pi` からの agent status、live UI / TTL / clear を確認してください。

ローカルテストはテスト専用 SDK stub、実 Unix socket、Go proxy / collector、日本語 request、実時間 TTL、validation、lifecycle、filesystem failure、subprocess cancellation を検証します。通常コードに架空の telemetry を注入しません。既知実機では telemetry / UI / PiSugar 読取が確認されていますが、空 image への導入、全 SDK、全 display / battery model は別途検証が必要です。

USB / PAN は [導入ガイド](../docs/install.ja.md)、[PAN guide](../docs/pan.md)、PC 側 API は [host document](../docs/host.md) を参照してください。USB の reboot 後 stall は未解決で、plugin や helper に自動 kernel reset はありません。
