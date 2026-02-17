use oasis_core::config::Config;
use std::path::Path;
use std::sync::Arc;

#[tokio::main]
async fn main() {
    let config_path = std::env::var("OASIS_CONFIG")
        .unwrap_or_else(|_| "oasis.toml".to_string());

    let config = Config::load(Path::new(&config_path)).unwrap_or_else(|e| {
        eprintln!("fatal: failed to load config: {e}");
        std::process::exit(1);
    });

    if config.telegram.token.is_empty() {
        eprintln!("fatal: OASIS_TELEGRAM_TOKEN is not set");
        std::process::exit(1);
    }

    eprintln!("oasis: starting...");

    let brain = Arc::new(
        oasis_brain::brain::Brain::new(config)
            .await
            .unwrap_or_else(|e| {
                eprintln!("fatal: failed to initialize brain: {e}");
                std::process::exit(1);
            }),
    );

    if let Err(e) = brain.run().await {
        eprintln!("fatal: brain error: {e}");
        std::process::exit(1);
    }
}
