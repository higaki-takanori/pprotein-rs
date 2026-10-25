//! `X-Git-Repository` ヘッダー用の git 情報取得。
//! Go 版 (`internal/git/repository.go`) と同じ JSON 形式を返す。

use std::path::Path;
use std::process::Command;

use serde::Serialize;

#[derive(Debug, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct RepositoryInfo {
    pub r#ref: String,
    pub hash: String,
    pub author: String,
    pub message: String,
    pub remote: String,
}

/// 環境変数 `PPROTEIN_GIT_REPOSITORY`（既定: カレントディレクトリ）のリポジトリパス
pub fn repository_path() -> String {
    std::env::var("PPROTEIN_GIT_REPOSITORY")
        .ok()
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| ".".to_string())
}

fn git(dir: &Path, args: &[&str]) -> Option<String> {
    let out = Command::new("git").arg("-C").arg(dir).args(args).output().ok()?;
    if !out.status.success() {
        return None;
    }
    String::from_utf8(out.stdout).ok()
}

/// HEAD のコミット情報を取得する。親ディレクトリも遡って探す。
/// リポジトリや origin が無い場合は `None`（Go 版と同じくヘッダーを付けない）。
pub fn get_info(dir: impl AsRef<Path>) -> Option<RepositoryInfo> {
    let dir = dir.as_ref();
    let hash = git(dir, &["rev-parse", "HEAD"])?.trim().to_string();
    // detached HEAD の場合は go-git と同じく "HEAD"
    let r#ref = git(dir, &["symbolic-ref", "-q", "HEAD"])
        .map(|s| s.trim().to_string())
        .unwrap_or_else(|| "HEAD".to_string());
    let author = git(dir, &["log", "-1", "--format=%an <%ae>"])?.trim().to_string();
    let message = git(dir, &["log", "-1", "--format=%B"])?.trim_end().to_string() + "\n";
    let remote = git(dir, &["config", "--get", "remote.origin.url"])?.trim().to_string();

    Some(RepositoryInfo { r#ref, hash, author, message, remote })
}

/// ヘッダー値として使う JSON 文字列
pub fn header_value() -> Option<String> {
    get_info(repository_path()).and_then(|info| serde_json::to_string(&info).ok())
}
