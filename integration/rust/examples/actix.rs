//! cargo run --release --example actix --features actix
//! curl -o profile.pb.gz 'http://localhost:9000/debug/pprof/profile?seconds=5'

use actix_web::{web, App, HttpServer};

async fn fib() -> String {
    fn f(n: u64) -> u64 {
        if n < 2 { n } else { f(n - 1) + f(n - 2) }
    }
    f(30).to_string()
}

#[actix_web::main]
async fn main() -> std::io::Result<()> {
    println!("listening on :9000");
    HttpServer::new(|| {
        App::new()
            .configure(pprotein_integration::actix::configure)
            .route("/", web::get().to(fib))
    })
    .bind(("0.0.0.0", 9000))?
    .run()
    .await
}
