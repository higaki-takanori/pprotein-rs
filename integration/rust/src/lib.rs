//! pprotein から Rust アプリの CPU プロファイルを収集するための integration。
//!
//! pprotein は `GET {URL}?seconds=N` で pprof 形式（protobuf）のプロファイルを取得する。
//! このクレートは pprof-rs でそれを生成し、axum / actix-web 向けのハンドラを提供する。
//!
//! - axum: `features = ["axum"]` → [`axum::router`]
//! - actix-web: `features = ["actix"]` → [`actix::configure`]

use std::fmt;
use std::io::Write;
use std::time::Duration;

use flate2::write::GzEncoder;
use flate2::Compression;
use pprof::protos::Message;

pub mod git;

#[cfg(feature = "actix")]
pub mod actix;
#[cfg(feature = "axum")]
pub mod axum;

/// `seconds` 未指定時の計測時間（Go の net/http/pprof と同じ）
pub const DEFAULT_SECONDS: u64 = 30;
/// サンプリング周波数（Go の runtime/pprof と同じ 100Hz）
pub const SAMPLING_FREQUENCY: i32 = 100;

/// 計測対象から除外するライブラリ（シグナルハンドラ内でのデッドロック回避）
const BLOCKLIST: &[&str] = &["libc", "libgcc", "pthread", "vdso"];

/// レスポンスに付与する git 情報ヘッダー名
pub const GIT_HEADER: &str = "X-Git-Repository";

#[derive(Debug)]
pub struct Error(String);

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

impl Error {
    fn new(msg: impl fmt::Display) -> Self {
        Self(msg.to_string())
    }
}

/// クエリ文字列の `seconds` を解釈する（未指定・0・不正値は既定値）
pub fn parse_seconds(raw: Option<&str>) -> u64 {
    raw.and_then(|s| s.parse::<u64>().ok())
        .filter(|&s| s > 0)
        .unwrap_or(DEFAULT_SECONDS)
}

/// 指定秒数だけ計測し、レポートを返す。
/// 計測中はブロッキングするので専用スレッドで実行する。
async fn collect_report(seconds: u64) -> Result<pprof::Report, Error> {
    tokio::task::spawn_blocking(move || {
        let guard = pprof::ProfilerGuardBuilder::default()
            .frequency(SAMPLING_FREQUENCY)
            .blocklist(BLOCKLIST)
            .build()
            .map_err(|e| Error::new(format!("could not enable CPU profiling: {e}")))?;

        std::thread::sleep(Duration::from_secs(seconds));

        guard
            .report()
            .build()
            .map_err(|e| Error::new(format!("failed to build report: {e}")))
    })
    .await
    .map_err(|e| Error::new(format!("profiling task failed: {e}")))?
}

/// CPU プロファイルを計測し、gzip 圧縮済みの pprof protobuf を返す。
pub async fn profile(seconds: u64) -> Result<Vec<u8>, Error> {
    let report = collect_report(seconds).await?;
    let profile = report
        .pprof()
        .map_err(|e| Error::new(format!("failed to convert to pprof: {e}")))?;

    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    encoder
        .write_all(&profile.encode_to_vec())
        .and_then(|_| encoder.finish())
        .map_err(|e| Error::new(format!("failed to compress profile: {e}")))
}

/// CPU プロファイルを計測し、フレームグラフ SVG を返す。
pub async fn flamegraph(seconds: u64) -> Result<Vec<u8>, Error> {
    let report = collect_report(seconds).await?;
    let mut svg = Vec::new();
    report
        .flamegraph(&mut svg)
        .map_err(|e| Error::new(format!("failed to render flamegraph: {e}")))?;
    Ok(svg)
}
