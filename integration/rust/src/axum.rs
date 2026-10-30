//! axum 向け integration。
//!
//! ```ignore
//! let app = Router::new()
//!     .route("/", get(handler))
//!     .merge(pprotein_integration::axum::router());
//! ```

use std::collections::HashMap;

use axum::extract::{Query, Request};
use axum::http::{header, HeaderValue, StatusCode};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::Router;

/// `/debug/pprof/profile` と `/debug/pprof/flamegraph` を持つルーター。
/// アプリのルーターに `merge` して使う。
pub fn router<S>() -> Router<S>
where
    S: Clone + Send + Sync + 'static,
{
    Router::new()
        .route("/debug/pprof/profile", get(profile))
        .route("/debug/pprof/flamegraph", get(flamegraph))
        .layer(middleware::from_fn(git_header))
}

fn seconds(q: &HashMap<String, String>) -> u64 {
    crate::parse_seconds(q.get("seconds").map(String::as_str))
}

fn error(e: crate::Error) -> Response {
    (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response()
}

async fn profile(Query(q): Query<HashMap<String, String>>) -> Response {
    match crate::profile(seconds(&q)).await {
        Ok(body) => (
            [
                (header::CONTENT_TYPE, "application/octet-stream"),
                (header::CONTENT_DISPOSITION, "attachment; filename=\"profile\""),
            ],
            body,
        )
            .into_response(),
        Err(e) => error(e),
    }
}

async fn flamegraph(Query(q): Query<HashMap<String, String>>) -> Response {
    match crate::flamegraph(seconds(&q)).await {
        Ok(body) => ([(header::CONTENT_TYPE, "image/svg+xml")], body).into_response(),
        Err(e) => error(e),
    }
}

async fn git_header(req: Request, next: Next) -> Response {
    let mut res = next.run(req).await;
    let value = tokio::task::spawn_blocking(crate::git::header_value)
        .await
        .ok()
        .flatten();
    if let Some(v) = value.and_then(|v| HeaderValue::from_str(&v).ok()) {
        res.headers_mut().insert(crate::GIT_HEADER, v);
    }
    res
}
