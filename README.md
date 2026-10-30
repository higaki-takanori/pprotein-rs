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

#### nginx / MySQL のログを出す

pprotein-agent はログファイルを読むだけなので、nginx と MySQL 側でログを出す設定が必要です。設定例は `mock/nginx.conf` / `mock/mysqld.cnf` にもあります。

**nginx**: alp が読める LTSV 形式でアクセスログを出します。`/etc/nginx/nginx.conf` の `http { ... }` 内にある既存の `access_log` を置き換えます。

```nginx
http {
    log_format ltsv "time:$time_local"
        "\thost:$remote_addr"
        "\tforwardedfor:$http_x_forwarded_for"
        "\treq:$request"
        "\tmethod:$request_method"
        "\turi:$request_uri"
        "\tstatus:$status"
        "\tsize:$body_bytes_sent"
        "\treferer:$http_referer"
        "\tua:$http_user_agent"
        "\treqtime:$request_time"
        "\truntime:$upstream_http_x_runtime"
        "\tapptime:$upstream_response_time"
        "\tcache:$upstream_http_x_cache"
        "\tvhost:$host";

    access_log /var/log/nginx/access.log ltsv;
    # ...
}
```

```sh
sudo nginx -t && sudo systemctl reload nginx
```

**MySQL**: スローログを有効にします。`long_query_time=0` にすると全クエリが記録されます。`/etc/mysql/mysql.conf.d/mysqld.cnf`（MariaDB なら `/etc/mysql/mariadb.conf.d/50-server.cnf`）の `[mysqld]` に追記します。

```ini
[mysqld]
slow_query_log=ON
slow_query_log_file=/var/log/mysql/mysql-slow.log
long_query_time=0
```

```sh
sudo systemctl restart mysql
```

再起動したくない場合は、SQL で一時的に有効にもできます（MySQL を再起動すると元に戻ります）。

```sql
SET GLOBAL slow_query_log_file = '/var/log/mysql/mysql-slow.log';
SET GLOBAL long_query_time = 0;
SET GLOBAL slow_query_log = ON;
```

`long_query_time` の変更は、変更後に接続したセッションから効きます。アプリも再起動してください。

スローログは既定で mysql ユーザーしか読めないことがあります。pprotein-agent を動かすユーザーが読めるか `ls -l /var/log/mysql/` で確認し、読めなければ `sudo chmod +r /var/log/mysql/mysql-slow.log` などで権限を付けてください。

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

手で始める場合は、`curl` の代わりに画面のグループ一覧にある **Collect** ボタンでも同じことができます。各一覧（pprof / httplog / slowlog）から個別に始めることもできます。

#### initialize で自動的に計測を始める

ISUCON のベンチマーカーは、毎回最初にアプリの初期化エンドポイント（`POST /initialize` など）を呼びます。その中で上の collect を送れば、ポータルでベンチマークを実行するだけで計測も始まります。ベンチマークサーバーには手を加えず、変えるのは自分たちのアプリだけです。

```
ポータルでベンチマーク実行
  → ベンチマーカーがアプリに POST /initialize
    → アプリが pprotein に GET /api/group/collect（ここを追加する）
      → Duration 秒間の計測が始まる
```

**1. アプリから pprotein に届くようにする**

| pprotein の置き場所 | アプリから見た URL |
| --- | --- |
| アプリと同じサーバー | `http://localhost:9000/api/group/collect` |
| 別のサーバー（監視用や競技サーバーの 1 台） | `http://<そのサーバーのプライベート IP>:9000/api/group/collect` |
| 手元の PC | 下の `ssh -R` でつないで `http://localhost:9000/api/group/collect` |

手元の PC で動かしている場合、普通はサーバーから手元の 9000 番には届きません。SSH に `-R` を付けると、サーバーの `localhost:9000` が手元の pprotein につながります（SSH を切るとつながらなくなります）。

```sh
ssh -R 9000:localhost:9000 \
    -L 8080:localhost:8080 -L 19000:localhost:19000 \
    isucon@<計測対象>
```

サーバー上で `curl -i http://localhost:9000/api/group/collect` が `200 OK` を返せばつながっています（このとき空のグループが 1 つできます）。

**2. initialize ハンドラに追加する**

Rust（axum / actix-web）の例です。`Cargo.toml` に reqwest を追加します。

```toml
[dependencies]
reqwest = { version = "0.12", default-features = false }
```

```rust
async fn initialize(/* ... */) -> impl IntoResponse {
    // 既存の初期化処理（DB のリセットなど）
    // ...

    // 計測開始。ベンチマークを待たせないよう裏で送り、失敗しても無視する
    tokio::spawn(async {
        let Ok(url) = std::env::var("PPROTEIN_COLLECT_URL") else { return };
        let Ok(client) = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(3))
            .build()
        else { return };
        let _ = client.get(url).send().await;
    });

    Json(json!({ "language": "rust" }))
}
```

Go の場合:

```go
go func() {
    url := os.Getenv("PPROTEIN_COLLECT_URL")
    if url == "" {
        return
    }
    c := http.Client{Timeout: 3 * time.Second}
    if resp, err := c.Get(url); err == nil {
        resp.Body.Close()
    }
}()
```

アプリの環境変数に URL を設定します（systemd ならユニットファイルの `[Service]` に `Environment=PPROTEIN_COLLECT_URL=http://localhost:9000/api/group/collect` を追記するか、アプリが読む env ファイルに書きます）。設定したらアプリを再起動します。

- **裏で送って待たない**: initialize の応答が遅れるとベンチマークが失敗することがあります。pprotein 側もリクエストを受けたらすぐ返し、収集は裏で進めます。
- **タイムアウトを付け、エラーは無視する**: pprotein が止まっていてもアプリに影響しないようにします。
- **URL は環境変数にする**: 環境変数を消せば計測が止まります。最終計測の前は消すか、このコードごと外してください。

**3. Duration を調整する**

initialize は負荷がかかる前に呼ばれるので、計測はそこから始まります。setting の `Duration` はベンチマークの実行時間より少し長めにします（例: ベンチマークが 60 秒なら 70 秒）。短いと終盤が計測から外れます。

前の計測が終わる前に次のベンチマークを始めると、計測が重なります。`Duration` 秒たってから次を実行してください。

### 3. 結果を見る

`Duration` 秒たつと収集が終わり、画面に結果が並びます。

- **pprof**: Flame Graph / Top などで、CPU 時間を使っている関数を確認できます。
- **httplog**: エンドポイント別のリクエスト数・レスポンス時間の集計
- **slowlog**: クエリ別の実行回数・実行時間の集計

各結果には、収集した時点のアプリの git コミットも表示されます。改善前後の比較に使えます。

### 4. 結果を AI に渡す（レポート API）

1 回の計測（`/api/group/collect` で集めた 1 グループ）の httplog / slowlog / pprof を、1 つの Markdown にまとめて返します。合計時間の多い順に上位だけを載せるので、そのまま AI に渡せます。

```sh
# 最新の計測結果
curl "http://<pprotein のホスト>:9000/api/report/latest"

# 前回の計測との差分も付ける。上位 20 件まで
curl "http://<pprotein のホスト>:9000/api/report/latest?compare=prev&top=20"

# Claude Code ならそのまま読ませられる
! curl -s "http://localhost:9000/api/report/latest?compare=prev"
```

| パラメータ | 内容 |
|---|---|
| `:group` | グループ ID。`latest` で最新 |
| `top` | 各表の行数（既定 15） |
| `compare` | `prev` で直前のグループと比べる。グループ ID も指定できる |
| `format` | `json` で JSON を返す（既定は Markdown） |

レポートに入る内容:

- **HTTP**: エンドポイント別の合計時間（Sum）の多い順。全体に占める割合、件数、平均、P99、4xx / 5xx の件数
- **Slow queries**: クエリ別の合計時間の多い順。割合、件数、平均、平均の参照行数（Rows examined）
- **Profile**: 関数別の flat（関数自身の時間）と cum（呼び出し先を含む時間）の上位
- **Diff**（`compare` 指定時）: 前回からの変化が大きい順。マイナスが改善
- git コミット、処理中・失敗で集計に入らなかったもの

memo は AI に渡すとノイズになるので、レポートにも一覧にも含めません（Web 画面では見られます）。

グループの一覧は `GET /api/report` で取れます（新しい順）。

### 5. AI エージェントから使う（MCP）

pprotein は `/mcp` で MCP サーバー（Streamable HTTP）としても動きます。AI エージェントが自分で一覧やレポートを取り、気になるエンドポイントやクエリを深掘りできます。

```sh
# Claude Code に登録する
claude mcp add --transport http pprotein http://localhost:9000/mcp
```

pprotein が別のマシンにある場合は、URL をそのホストにするか、SSH ポートフォワードで手元の 9000 番につなぎます。

| ツール | 内容 |
|---|---|
| `list_groups` | 計測（グループ）の一覧。新しい順 |
| `get_report` | 1 回分のレポート（`/api/report/:group` と同じ）。`compare: "prev"` で差分付き |
| `get_diff` | 2 回分の差分だけ。改善の確認に |
| `get_http_stats` | エンドポイント別の集計。`sort`（sum / count / avg / p99 / max / 5xx）と `filter`（正規表現）で絞り込み |
| `get_slow_queries` | クエリ別の集計。`sort`（sum / count / avg / max / rows_examined）と `filter`。クエリ全文も返す |
| `get_profile` | pprof の関数別集計。`sort`（flat / cum）と `filter`（例: `^main\.` やクレート名で自分のコードだけにする） |
| `add_memo` | グループに memo を追加する（スコアや変更内容を Web 画面に残す用。ツールの応答には出ない） |
| `start_collect` | 登録済みの全対象の計測を始める（`/api/group/collect` と同じ） |

`group` を省略すると最新のグループが対象です。

## 計測をやめる（agent を外す）

競技の最終計測の前などに、計測対象サーバーから pprotein-agent を取り除く手順です。

### 1. pprotein-agent を止めて削除する

systemd に登録した場合は、停止・自動起動の解除をしてからユニットファイルを消します。

```sh
sudo systemctl disable --now pprotein-agent
sudo rm /etc/systemd/system/pprotein-agent.service
sudo systemctl daemon-reload
```

手で起動していた場合は、そのプロセスを止めます（`Ctrl+C` か `pkill pprotein-agent`）。

最後にバイナリを消します。

```sh
rm ~/pprotein-agent
```

`systemctl status pprotein-agent` が `could not be found` になり、`curl http://localhost:19000/` がつながらなければ外れています。

### 2. アプリから integration を外す

integration を組み込んだままだと、アプリに `/debug/pprof/*` が残ります。不要になったら追加した部分を元に戻して、アプリをビルドし直します。

- Rust: `Cargo.toml` の `pprotein-integration` と、ルーターに追加した 1 行（`.merge(pprotein_integration::axum::router())` / `.configure(pprotein_integration::actix::configure)`）を消す。`[profile.release]` の `debug = 1` も不要なら消す
- Go: `echov4.Integrate(e)` などの呼び出しと import を消す
- initialize に collect を追加していた場合は、そのコードと環境変数 `PPROTEIN_COLLECT_URL` も消す（Rust なら `reqwest` が不要になれば `Cargo.toml` からも消す）

### 3. pprotein 側の登録を消す

pprotein の **setting** から、そのサーバーの対象（`Label` が同じもの）を削除します。残っていると、計測開始のたびに接続エラーになります。

### 4. nginx / MySQL のログ設定を戻す

ログの出力自体も負荷になるので、最終計測の前には止めておくのがおすすめです。

**nginx**: `access_log` をオフにします（ログを残したい場合は `ltsv` を外して既定の形式に戻します）。`log_format ltsv` の定義は残っていても問題ありません。

```nginx
http {
    access_log off;
    # access_log /var/log/nginx/access.log;   # 残す場合
}
```

```sh
sudo nginx -t && sudo systemctl reload nginx
```

**MySQL**: 追記した 3 行を消すか、`slow_query_log=OFF` にして再起動します。

```ini
[mysqld]
slow_query_log=OFF
```

```sh
sudo systemctl restart mysql
```

再起動せずに止める場合は SQL で止めます（設定ファイルを直さないと、次の再起動で有効に戻ります）。

```sql
SET GLOBAL slow_query_log = OFF;
SET GLOBAL long_query_time = 10;   -- 既定値に戻す
```

`SHOW VARIABLES LIKE 'slow_query_log';` が `OFF` なら止まっています。たまったログファイルが不要なら消してもかまいません。

## Rust 版の制限

- 計測できるのは CPU プロファイルのみです（ヒープなどは非対応）。
- SIGPROF によるサンプリングのため、Linux での利用を想定しています。
- async タスクの await 待ち時間は計測されません。

## ライセンス

[LICENSE](LICENSE) を参照してください。
