//! cargo run --release --example axum --features axum
//! curl -o profile.pb.gz 'http://localhost:9000/debug/pprof/profile?seconds=5'

use axum::routing::get;
use axum::Router;

async fn fib(n: u64) -> String {
    fn f(n: u64) -> u64 {
        if n < 2 { n } else { f(n - 1) + f(n - 2) }
    }
    f(n).to_string()
}

#[tokio::main]
async fn main() {
    let app = Router::new()
        .route("/", get(|| fib(30)))
        .merge(pprotein_integration::axum::router());

    let listener = tokio::net::TcpListener::bind("0.0.0.0:9000").await.unwrap();
    println!("listening on :9000");
    axum::serve(listener, app).await.unwrap();
}
