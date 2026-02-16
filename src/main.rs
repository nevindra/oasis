use oasis_core::config::Config;
use oasis_core::error::OasisError;
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

    // Create brain (owns its own DB, task manager, bot, etc.)
    let brain = Arc::new(
        oasis_brain::brain::Brain::new(config.clone())
            .await
            .unwrap_or_else(|e| {
                eprintln!("fatal: failed to initialize brain: {e}");
                std::process::exit(1);
            }),
    );

    // Create scheduler (separate DB handle, task manager, and bot instance)
    let scheduler = create_scheduler(&config).await;

    // Run brain and scheduler in parallel
    tokio::select! {
        result = brain.run() => {
            if let Err(e) = result {
                eprintln!("fatal: brain error: {e}");
                std::process::exit(1);
            }
        }
        result = async {
            match scheduler {
                Some(s) => s.run().await,
                None => {
                    // No scheduler — just wait forever
                    std::future::pending::<oasis_core::error::Result<()>>().await
                }
            }
        } => {
            if let Err(e) = result {
                eprintln!("fatal: scheduler error: {e}");
                std::process::exit(1);
            }
        }
    }
}

/// Create the scheduler with its own DB handle and bot instance.
/// Returns None if initialization fails (non-fatal — brain runs without scheduler).
async fn create_scheduler(
    config: &Config,
) -> Option<oasis_brain::scheduler::Scheduler> {
    // Open a separate DB handle for the scheduler
    let db = if !config.database.turso_url.is_empty() {
        libsql::Builder::new_remote(
            config.database.turso_url.clone(),
            config.database.turso_token.clone(),
        )
        .build()
        .await
        .map_err(|e| OasisError::Database(e.to_string()))
    } else {
        libsql::Builder::new_local(&config.database.path)
            .build()
            .await
            .map_err(|e| OasisError::Database(e.to_string()))
    };

    let db = match db {
        Ok(d) => d,
        Err(e) => {
            eprintln!("oasis: scheduler DB init failed (non-fatal): {e}");
            return None;
        }
    };

    // Separate DB handle for the task manager
    let task_db = if !config.database.turso_url.is_empty() {
        libsql::Builder::new_remote(
            config.database.turso_url.clone(),
            config.database.turso_token.clone(),
        )
        .build()
        .await
    } else {
        libsql::Builder::new_local(&config.database.path)
            .build()
            .await
    };

    let task_db = match task_db {
        Ok(d) => d,
        Err(e) => {
            eprintln!("oasis: scheduler task DB init failed (non-fatal): {e}");
            return None;
        }
    };

    let tasks = oasis_brain::tasks::TaskManager::new(task_db);

    // Separate bot instance for the scheduler
    let bot = oasis_telegram::bot::TelegramBot::new(
        config.telegram.token.clone(),
        config.telegram.allowed_user_id,
    );

    // Resolve owner chat_id from the config DB
    // The scheduler needs the owner's chat_id to send proactive messages.
    // We'll try to read it from the config table; if not set yet (first run),
    // the scheduler will start but won't send messages until the owner registers.
    let chat_id = {
        let conn = match db.connect() {
            Ok(c) => c,
            Err(e) => {
                eprintln!("oasis: scheduler conn failed (non-fatal): {e}");
                return None;
            }
        };
        let mut rows = conn
            .query(
                "SELECT value FROM config WHERE key = 'owner_user_id'",
                (),
            )
            .await
            .ok()?;
        rows.next().await.ok()?.and_then(|row| {
            row.get::<String>(0).ok()?.parse::<i64>().ok()
        })
    };

    let chat_id = match chat_id {
        Some(id) => id,
        None => {
            eprintln!("oasis: no owner registered yet, scheduler will wait for first message");
            0 // Will be set once owner registers
        }
    };

    let scheduler =
        oasis_brain::scheduler::Scheduler::new(db, tasks, bot, chat_id, config.brain.timezone_offset);

    if let Err(e) = scheduler.init().await {
        eprintln!("oasis: scheduler table init failed (non-fatal): {e}");
        return None;
    }

    Some(scheduler)
}
