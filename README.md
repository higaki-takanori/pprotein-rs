# pprotein-rs

ISUCON 向けの計測ツール [pprotein](https://github.com/kaz/pprotein) に、**Rust アプリの CPU プロファイル計測**を追加したフォークです。

| 種類 | 内容 |
| --- | --- |
| pprof | アプリの CPU プロファイル |
| httplog | nginx のアクセスログ（alp で集計） |
| slowlog | MySQL のスロークエリログ（slp で集計） |

## 構成

手元の Mac で `pprotein` を、計測対象の Linux サーバーで `pprotein-agent` とアプリを動かします。Mac とサーバーは SSH のポートフォワードでつなぎます。

```
Mac                                   Linux サーバー
┌────────────────┐   SSH でつなぐ    ┌──────────────────────────┐
│ ./pprotein     │ ◀──────────────▶ │ アプリ    :8080（例）      │
│ :9000          │                  │ ./pprotein-agent :19000  │
└────────────────┘                  │ nginx / MySQL のログ      │
                                    └──────────────────────────┘
```

各手順の先頭に、作業する場所を **【Mac】** / **【サーバー】** で書いています。

## 準備

### 1. pprotein を起動する【Mac】

Go と Node.js（npm）が必要です。

```sh
# 【Mac】
brew install graphviz
go install github.com/tkuchiki/alp/cmd/alp@latest
go install github.com/tkuchiki/slp/cmd/slp@latest
which alp slp dot   # 3 つとも表示されれば OK（出なければ ~/go/bin を PATH に追加）

git clone https://github.com/higaki-takanori/pprotein-rs.git
cd pprotein-rs
make build
./pprotein          # http://localhost:9000
```

### 2. pprotein-agent をサーバーに置く【Mac → サーバー】

サーバーの CPU を確認します。

```sh
# 【サーバー】
uname -m            # x86_64 または aarch64
```

Mac で Linux 向けにビルドして送ります。

```sh
# 【Mac】pprotein-rs のディレクトリで
make agent-linux                  # x86_64 の場合
make agent-linux GOARCH=arm64     # aarch64 の場合

scp pprotein-agent-linux-amd64 isucon@<サーバー>:~/pprotein-agent   # arm64 なら pprotein-agent-linux-arm64
```

systemd に登録して起動します。

```ini
# 【サーバー】/etc/systemd/system/pprotein-agent.service
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
# 【サーバー】
chmod +x ~/pprotein-agent
sudo systemctl daemon-reload
sudo systemctl enable --now pprotein-agent
```

`WorkingDirectory` をアプリの git リポジトリにしておくと、結果にコミットが表示されます。

### 3. nginx / MySQL のログを出す【サーバー】

**nginx**: `/etc/nginx/nginx.conf` の `http { ... }` にある `access_log` を LTSV 形式に置き換えます。

```nginx
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
```

**MySQL**: `/etc/mysql/mysql.conf.d/mysqld.cnf` の `[mysqld]` に追記します。

```ini
slow_query_log=ON
slow_query_log_file=/var/log/mysql/mysql-slow.log
long_query_time=0
```

```sh
# 【サーバー】
sudo nginx -t && sudo systemctl reload nginx
sudo systemctl restart mysql
sudo chmod +r /var/log/mysql/mysql-slow.log   # agent が読めるようにする
```

ログの場所が違う場合は、agent の環境変数 `PPROTEIN_HTTPLOG` / `PPROTEIN_SLOWLOG` で指定します。

### 4. アプリに組み込む【アプリのリポジトリ】

**Rust（axum / actix-web）**

```toml
[dependencies]
pprotein-integration = { git = "https://github.com/higaki-takanori/pprotein-rs", features = ["axum"] }  # actix-web なら "actix"

[profile.release]
debug = 1   # 関数名をプロファイルに出すため
```

```rust
// axum: 既存のルーターに .merge(...) の 1 行を足すだけ
let app = Router::new()
    .route("/", get(handler))                      // ← 既存のルート（変更不要）
    .merge(pprotein_integration::axum::router())   // ← 追加
    .with_state(state);                            // ← 既存（あれば）

// actix-web: 既存の App に .configure(...) の 1 行を足すだけ
App::new()
    .configure(pprotein_integration::actix::configure)   // ← 追加
    .route("/", web::get().to(handler))                  // ← 既存のルート（変更不要）
```

**Go（echo v4 の例）**: `echov4.Integrate(e)`。gin / mux などは `integration/` 以下にあります。

組み込んだらサーバーでビルド・再起動します。詳細は [integration/rust/README.md](integration/rust/README.md) を参照してください。

## 計測する

以下ではアプリのポートを `8080` としています。実際のポートはサーバーで確認し、違う場合は以下の `8080` をすべて置き換えてください。

```sh
# 【サーバー】
grep -rn proxy_pass /etc/nginx/   # nginx がアプリに転送している先
sudo ss -ltnp                     # 待ち受けているポートとプロセス名
```

### 1. Mac とサーバーをつなぐ【Mac】

```sh
# 【Mac】計測中はこの SSH を切らない
ssh -L 8080:localhost:8080 -L 19000:localhost:19000 -R 9000:localhost:9000 isucon@<サーバー>
```

- `-L 8080` / `-L 19000`: Mac の pprotein が、サーバーのアプリと agent からデータを取るため
- `-R 9000`: サーバーのアプリが、Mac の pprotein に計測開始を伝えるため（下の「自動で始める」用）

### 2. 収集対象を登録する【Mac】

ブラウザで `http://localhost:9000` を開き、**setting** に登録します。

```json
[
  { "Type": "pprof",   "Label": "app", "URL": "http://localhost:8080/debug/pprof/profile", "Duration": 70 },
  { "Type": "httplog", "Label": "app", "URL": "http://localhost:19000/debug/log/httplog",  "Duration": 70 },
  { "Type": "slowlog", "Label": "app", "URL": "http://localhost:19000/debug/log/slowlog",  "Duration": 70 }
]
```

`Duration` はベンチマークの時間より少し長くします（60 秒なら 70）。

pprof の URL は**アプリのポート**にします。`19000/debug/pprof/profile` も 200 を返しますが、それは pprotein-agent 自身（Go）のプロファイルで、アプリの計測にはなりません。

### 3. 計測を始める

**手で始める【Mac】**: ベンチマーク開始と同時に、画面の **Collect** ボタンを押すか、次を実行します。

```sh
curl http://localhost:9000/api/group/collect
```

**自動で始める【アプリのリポジトリ → サーバー】**: ベンチマーカーは最初に `POST /initialize` を呼ぶので、そこで collect を送ればボタンは不要です。

```rust
// initialize ハンドラの最後に追加（reqwest が必要）
tokio::spawn(async {
    let Ok(url) = std::env::var("PPROTEIN_COLLECT_URL") else { return };
    let Ok(client) = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(3))
        .build()
    else { return };
    let _ = client.get(url).send().await;
});
```

次に、アプリの環境変数 `PPROTEIN_COLLECT_URL` を設定します【サーバー】。値はビルド時ではなく起動時に読まれるので、Cargo.toml ではなく、アプリを起動している systemd のサービスに設定します。

```sh
# サービス名を探す（問題ごとに違う）
systemctl list-units --type=service | grep isu

# 読み込んでいる env ファイルを確認する
systemctl cat <サービス名>
```

`EnvironmentFile=`（例: `/home/isucon/env.sh`）があれば、そこに追記して再起動します。

```sh
echo 'PPROTEIN_COLLECT_URL=http://localhost:9000/api/group/collect' >> /home/isucon/env.sh
sudo systemctl restart <サービス名>
```

`EnvironmentFile=` がなければ、`sudo systemctl edit <サービス名>` で次を書いて保存し、再起動します。

```ini
[Service]
Environment=PPROTEIN_COLLECT_URL=http://localhost:9000/api/group/collect
```

サーバーの `localhost:9000` は、手順 1 の `-R` によって Mac の pprotein につながっています。この変数を設定していなければ、追加したコードは何もしません。

サーバーで `curl -i http://localhost:9000/api/group/collect` が `200 OK` になれば届いています。

### 4. 結果を見る【Mac】

`Duration` 秒たつと、`http://localhost:9000` に結果が並びます。

AI に渡す場合は、1 回分をまとめた Markdown を取れます。

```sh
curl "http://localhost:9000/api/report/latest?compare=prev"   # 前回との差分つき
```

Claude Code からは MCP でも使えます。

```sh
claude mcp add --transport http pprotein http://localhost:9000/mcp
```

## 計測をやめる

最終計測の前に外します。

```sh
# 【サーバー】
sudo systemctl disable --now pprotein-agent
sudo rm /etc/systemd/system/pprotein-agent.service ~/pprotein-agent
sudo systemctl daemon-reload
```

- アプリから integration と initialize の collect を消し、ビルド・再起動する
- env ファイルまたは `systemctl edit` で追加した `PPROTEIN_COLLECT_URL` を消す
- nginx の `access_log` を `off` に、MySQL の `slow_query_log` を `OFF` にして再起動する

## Rust 版の制限

- 計測できるのは CPU プロファイルのみです。
- Linux での利用を想定しています。
- async タスクの await 待ち時間は計測されません。

## ライセンス

[LICENSE](LICENSE) を参照してください。
