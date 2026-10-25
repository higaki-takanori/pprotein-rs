//! actix-web 向け integration。
//!
//! ```ignore
//! App::new()
//!     .configure(pprotein_integration::actix::configure)
//!     .route("/", web::get().to(handler))
//! ```

use std::collections::HashMap;

use actix_web::body::MessageBody;
use actix_web::dev::{ServiceRequest, ServiceResponse};
use actix_web::http::header::{HeaderName, HeaderValue};
use actix_web::middleware::{from_fn, Next};
use actix_web::{web, HttpResponse};

/// `/debug/pprof/profile` と `/debug/pprof/flamegraph` を登録する。
/// `App::configure` に渡して使う。
pub fn configure(cfg: &mut web::ServiceConfig) {
    cfg.service(
        web::scope("/debug/pprof")
            .wrap(from_fn(git_header))
            .route("/profile", web::get().to(profile))
            .route("/flamegraph", web::get().to(flamegraph)),
    );
}

fn seconds(q: &HashMap<String, String>) -> u64 {
    crate::parse_seconds(q.get("seconds").map(String::as_str))
}

fn error(e: crate::Error) -> HttpResponse {
    HttpResponse::InternalServerError().body(e.to_string())
}

async fn profile(q: web::Query<HashMap<String, String>>) -> HttpResponse {
    match crate::profile(seconds(&q)).await {
        Ok(body) => HttpResponse::Ok()
            .content_type("application/octet-stream")
            .insert_header(("Content-Disposition", "attachment; filename=\"profile\""))
            .body(body),
        Err(e) => error(e),
    }
}

async fn flamegraph(q: web::Query<HashMap<String, String>>) -> HttpResponse {
    match crate::flamegraph(seconds(&q)).await {
        Ok(body) => HttpResponse::Ok().content_type("image/svg+xml").body(body),
        Err(e) => error(e),
    }
}

async fn git_header(
    req: ServiceRequest,
    next: Next<impl MessageBody>,
) -> Result<ServiceResponse<impl MessageBody>, actix_web::Error> {
    let mut res = next.call(req).await?;
    let value = web::block(crate::git::header_value).await.ok().flatten();
    if let Some(v) = value.and_then(|v| HeaderValue::from_str(&v).ok()) {
        res.headers_mut()
            .insert(HeaderName::from_static("x-git-repository"), v);
    }
    Ok(res)
}
