# pprotein-rs

ISUCON 向けのパフォーマンス計測ツール [pprotein](https://github.com/kaz/pprotein) に、**Rust アプリの CPU プロファイル計測**を追加したフォークです。

pprotein は次の 3 種類のデータをまとめて収集し、ブラウザで閲覧できます。

| 種類 | 内容 | 解析ツール |
| --- | --- | --- |
| pprof | アプリの CPU プロファイル | [google/pprof](https://github.com/google/pprof) |
| httplog | nginx のアクセスログ | [alp](https://github.com/tkuchiki/alp) |
| slowlog | MySQL のスロークエリログ | [slp](https://github.com/tkuchiki/slp) |

Rust アプリでは [pprof-rs](https://github.com/tikv/pprof-rs) を使って Go と同じ pprof 形式のプロファイルを出力するため、pprotein 本体は Go 版と共通です。

## 構成

```
┌──────────────┐  収集リクエスト   ┌────────────────────────────────────┐
│  pprotein    │ ───────────────▶ │ 計測対象サーバー                       │
│  (本体/UI)   │                  │  ├ アプリ   /debug/pprof/profile     │
│  :9000       │ ◀─────────────── │  └ pprotein-agent :19000            │
└──────────────┘  プロファイル/ログ  │       /debug/log/httplog, slowlog  │
                                  └────────────────────────────────────┘
```

- **pprotein**: 収集・解析・閲覧を行う本体。計測対象とは別のマシン（手元の PC や監視用サーバー）に置くのがおすすめです。
- **pprotein-agent**: nginx / MySQL のログを配信する小さなサーバー。計測対象サーバーで動かします。
- **integration**: アプリに組み込んでプロファイルを返すライブラリ（Go / Rust）。

### どこで何を動かすか

`pprotein` と `pprotein-agent` は役割が決まっています。入れ替えては使えません。

| 置き場所 | 動かすもの | 必要なもの |
| --- | --- | --- |
| 手元の PC（または監視用サーバー） | `pprotein` | graphviz・alp・slp |
| 計測対象サーバー | `pprotein-agent` | nginx / MySQL のログファイル |
| 計測対象サーバー | アプリ（integration 組み込み済み） | — |

pprotein は**取りに行く（pull）方式**です。pprotein から計測対象サーバーのアプリ（例: 8080）と pprotein-agent（19000）にアクセスできる必要があります。計測対象サーバーからは何も送りません。

## 導入方法

### 1. pprotein 本体を起動する

ビルドには Go と Node.js（npm）が必要です。解析には `alp`・`slp`・`graphviz` を使うので、**pprotein を動かすマシン**に入れておきます。

```sh
# macOS の例
brew install graphviz
go install github.com/tkuchiki/alp/cmd/alp@latest
go install github.com/tkuchiki/slp/cmd/slp@latest
```

> `Could not execute dot; may need to install graphviz.` と出る場合は、graphviz が入っていません。インストール後に pprotein を再起動してください。

```sh
git clone https://github.com/higaki-takanori/pprotein-rs.git
cd pprotein-rs
make build          # pprotein と pprotein-agent をビルド
./pprotein          # http://localhost:9000 で起動（PORT 環境変数で変更可）
```

Docker を使う場合は alp / slp / graphviz も含めたイメージを作れます。

```sh
docker build -t pprotein .
docker run -p 9000:9000 -v $(pwd)/data:/opt/pprotein/data pprotein
```

収集したデータはカレントディレクトリの `data/` に保存されます。

### 2. 計測対象サーバーで pprotein-agent を起動する

`make build` で作ったバイナリは手元の OS 向けです（Mac なら macOS 用）。そのままサーバーにコピーしても動かないので、サーバーの OS / CPU に合わせてビルドし直してから送ります。

```sh
# 手元で Linux 向けにビルド → pprotein-agent-linux-amd64 ができる
make agent-linux
# サーバーが ARM の場合 → pprotein-agent-linux-arm64
make agent-linux GOARCH=arm64

# サーバーへ送る
scp pprotein-agent-linux-amd64 isucon@<計測対象>:~/pprotein-agent
```

サーバーの CPU は `uname -m` で確認できます（`x86_64` → `amd64`、`aarch64` → `arm64`）。

サーバーで起動します。nginx / MySQL のログを読むため、読み取り権限のあるユーザーで動かしてください。

```sh
./pprotein-agent    # :19000 で起動（PORT 環境変数で変更可）
```

ベンチマークのたびに起動し直さなくて済むよう、systemd に登録しておくと便利です。

```ini
# /etc/systemd/system/pprotein-agent.service
[Unit]
Description=pprotein-agent

[Service]
ExecStart=/home/isucon/pprotein-agent
WorkingDirectory=/home/isucon/webapp
Restart=always

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now pprotein-agent
```

`WorkingDirectory` はアプリの git リポジトリにしておくと、コミット情報が結果に表示されます。

ログファイルの場所は環境変数で変更できます。

| 環境変数 | 既定値 |
| --- | --- |
| `PPROTEIN_HTTPLOG` | `/var/log/nginx/access.log` |
| `PPROTEIN_SLOWLOG` | `/var/log/mysql/mysql-slow.log` |
| `PPROTEIN_GIT_REPOSITORY` | `.`（コミット情報の取得元） |

nginx のログは alp が読める LTSV 形式で出力しておく必要があります。

### 3. アプリに integration を組み込む

#### Rust（axum / actix-web）

`Cargo.toml` に追加します。使うフレームワークを feature で選びます。

```toml
[dependencies]
pprotein-integration = { git = "https://github.com/higaki-takanori/pprotein-rs", features = ["axum"] }
# actix-web の場合は features = ["actix"]

[profile.release]
debug = 1   # 関数名をプロファイルに出すために必要
```

ルーターに 1 行追加します。

```rust
// axum
let app = Router::new()
    .route("/", get(handler))
    .merge(pprotein_integration::axum::router());

// actix-web
App::new()
    .configure(pprotein_integration::actix::configure)
    .route("/", web::get().to(handler))
```

詳細は [integration/rust/README.md](integration/rust/README.md) を参照してください。

#### Go（echo / gin / gorilla/mux / net/http）

```go
import "github.com/kaz/pprotein/integration/echov4"

echov4.Integrate(e) // e は *echo.Echo
```

`integration/` 以下に echo v3 / echo v4 / gin / mux / standalone 用のパッケージがあります。

## 使い方

### 1. 収集対象を登録する

pprotein の画面（`http://<pprotein のホスト>:9000`）の **setting** で、収集対象を JSON で登録します。

```json
[
  { "Type": "pprof",   "Label": "app", "URL": "http://<計測対象>:8080/debug/pprof/profile", "Duration": 60 },
  { "Type": "httplog", "Label": "app", "URL": "http://<計測対象>:19000/debug/log/httplog",  "Duration": 60 },
  { "Type": "slowlog", "Label": "app", "URL": "http://<計測対象>:19000/debug/log/slowlog",  "Duration": 60 }
]
```

- `URL` の pprof は**アプリのポート**、httplog / slowlog は **pprotein-agent のポート**を指定します。
- `Duration` は計測する秒数です。ベンチマークの実行時間に合わせてください。
- サーバーが複数台ある場合は `Label` を変えて並べます。

#### 手元の PC で pprotein を動かす場合

計測対象サーバーの 8080 / 19000 番ポートが外から開いていない場合は、SSH のポートフォワードで手元につなぎます。

```sh
ssh -L 8080:localhost:8080 -L 19000:localhost:19000 isucon@<計測対象>
```

この場合、setting の `URL` は `http://localhost:8080/...`、`http://localhost:19000/...` を指定します。サーバーが複数台なら、手元のポート番号を台ごとに変えます（例: `-L 18080:localhost:8080`）。

### 2. 計測を開始する

ベンチマーク開始と同時に、登録したすべての対象の収集を始めます。

```sh
curl http://<pprotein のホスト>:9000/api/group/collect
```

ISUCON ではベンチマーカーが最初に叩く初期化エンドポイント（`POST /initialize` など）の中でこのリクエストを送ると、毎回自動で計測できます。

```rust
// 例: Rust アプリの initialize ハンドラ内で（reqwest を使う場合）
tokio::spawn(async {
    let _ = reqwest::get("http://<pprotein のホスト>:9000/api/group/collect").await;
});
```

ただし pprotein を手元の PC で動かしている場合、普通はサーバーから手元の 9000 番には届きません。その場合はベンチマーク開始と同時に、手元で上の `curl` を実行してください。

画面の各一覧から個別に収集を始めることもできます。

### 3. 結果を見る

`Duration` 秒たつと収集が終わり、画面に結果が並びます。

- **pprof**: Flame Graph / Top などで、CPU 時間を使っている関数を確認できます。
- **httplog**: エンドポイント別のリクエスト数・レスポンス時間の集計
- **slowlog**: クエリ別の実行回数・実行時間の集計

各結果には、収集した時点のアプリの git コミットも表示されます。改善前後の比較に使えます。

## Rust 版の制限

- 計測できるのは CPU プロファイルのみです（ヒープなどは非対応）。
- SIGPROF によるサンプリングのため、Linux での利用を想定しています。
- async タスクの await 待ち時間は計測されません。

## ライセンス

[LICENSE](LICENSE) を参照してください。
