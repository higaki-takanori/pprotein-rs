# pprotein-integration (Rust)

Rust 製の Web アプリから pprotein で CPU プロファイルを収集するための integration です。
[pprof-rs](https://github.com/tikv/pprof-rs) で Go と同じ pprof 形式のプロファイルを出力するため、pprotein 本体の変更は不要です。

## 導入

利用するフレームワークに合わせて feature を選びます。

```toml
[dependencies]
# axum の場合
pprotein-integration = { git = "https://github.com/higaki-takanori/pprotein-rs", features = ["axum"] }
# actix-web の場合
# pprotein-integration = { git = "https://github.com/higaki-takanori/pprotein-rs", features = ["actix"] }

# 関数名をプロファイルに出すため、release ビルドにもデバッグ情報を残す
[profile.release]
debug = 1
```

### axum

```rust
let app = Router::new()
    .route("/", get(handler))
    .merge(pprotein_integration::axum::router());
```

### actix-web

```rust
App::new()
    .configure(pprotein_integration::actix::configure)
    .route("/", web::get().to(handler))
```

git 依存はリポジトリ内のクレートを自動で探すため、サブディレクトリの指定は不要です。
ブランチやコミットを固定したい場合は `branch = "..."` / `rev = "..."` を追加してください。

## 計測までの流れ

1. 上記のとおりアプリに組み込み、`cargo build --release` で起動する
2. 動作確認: `curl -o profile.pb.gz 'http://localhost:<アプリのポート>/debug/pprof/profile?seconds=5'` が 200 で返ること
3. pprotein の setting 画面で、`Type: "pprof"` の URL にアプリの `/debug/pprof/profile` を登録する（下の httplog / slowlog の例を参照）
4. ベンチマーク開始時に `curl http://<pprotein のホスト>:9000/api/group/collect` を叩く
5. pprotein の画面で Flame Graph / Top を見る

pprotein 本体の起動方法などは、リポジトリ直下の [README](../../README.md) を参照してください。

## エンドポイント

| パス | 内容 |
| --- | --- |
| `/debug/pprof/profile?seconds=N` | CPU プロファイル（pprof 形式, gzip）。pprotein に登録するのはこれ |
| `/debug/pprof/flamegraph?seconds=N` | フレームグラフ SVG（ブラウザで直接見る用） |

`seconds` を省略すると 30 秒計測します。レスポンスには Go 版と同じ `X-Git-Repository` ヘッダーが付きます。
対象のリポジトリは環境変数 `PPROTEIN_GIT_REPOSITORY`（既定: カレントディレクトリ）で指定でき、`git` コマンドが必要です。

## httplog / slowlog

nginx / MySQL のログ配信は言語に依存しないため、Go 製の `pprotein-agent`（既定ポート 19000）を併用してください。

```json
[
  { "Type": "pprof",   "Label": "app", "URL": "http://localhost:8080/debug/pprof/profile", "Duration": 60 },
  { "Type": "httplog", "Label": "app", "URL": "http://localhost:19000/debug/log/httplog",  "Duration": 60 },
  { "Type": "slowlog", "Label": "app", "URL": "http://localhost:19000/debug/log/slowlog",  "Duration": 60 }
]
```

## 制限

- 計測できるのは CPU プロファイルのみです（ヒープなどは非対応）。
- SIGPROF によるサンプリングのため、Linux での利用を想定しています。
- 同時に計測できるのは 1 つだけです。計測中に別のリクエストが来ると 500 を返します。
- async タスクの await 待ち時間は計測されません（Go の fgprof 相当の機能はありません）。

## サンプル

```sh
cargo run --release --example axum --features axum
cargo run --release --example actix --features actix
curl -o profile.pb.gz 'http://localhost:9000/debug/pprof/profile?seconds=5'
go tool pprof -top profile.pb.gz
```
