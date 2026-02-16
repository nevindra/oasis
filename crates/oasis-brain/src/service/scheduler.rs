use libsql::Database;
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use super::tasks::TaskManager;
use oasis_telegram::bot::TelegramBot;
use std::time::Duration;

/// Notification tier — how far before the due date we send a reminder.
const TIER_24H: &str = "24h";
const TIER_1H: &str = "1h";
const TIER_DUE: &str = "due";
const TIER_OVERDUE: &str = "overdue";

/// Default interval between scheduler checks (1 minute).
const DEFAULT_CHECK_INTERVAL: Duration = Duration::from_secs(60);

fn db_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
}

/// Background scheduler that sends proactive reminders via Telegram.
///
/// Operates independently from Brain — reads tasks with due dates from the DB,
/// tracks which notifications have been sent, and sends Telegram messages.
pub struct Scheduler {
    db: Database,
    tasks: TaskManager,
    bot: TelegramBot,
    chat_id: i64,
    check_interval: Duration,
    tz_offset: i32,
}

impl Scheduler {
    pub fn new(db: Database, tasks: TaskManager, bot: TelegramBot, chat_id: i64, tz_offset: i32) -> Self {
        Self {
            db,
            tasks,
            bot,
            chat_id,
            check_interval: DEFAULT_CHECK_INTERVAL,
            tz_offset,
        }
    }

    fn conn(&self) -> Result<libsql::Connection> {
        self.db.connect().map_err(db_err)
    }

    /// Initialize the reminders table.
    pub async fn init(&self) -> Result<()> {
        self.conn()?
            .execute(
                "CREATE TABLE IF NOT EXISTS reminders (
                    id TEXT PRIMARY KEY,
                    task_id TEXT NOT NULL,
                    tier TEXT NOT NULL,
                    notified INTEGER DEFAULT 0,
                    created_at INTEGER NOT NULL
                )",
                (),
            )
            .await
            .map_err(db_err)?;
        Ok(())
    }

    /// Main scheduler loop. Runs indefinitely, checking for due reminders.
    pub async fn run(&self) -> Result<()> {
        eprintln!("oasis: scheduler started (interval: {:?})", self.check_interval);

        // On startup, check for any missed reminders
        if let Err(e) = self.check_and_notify().await {
            eprintln!("oasis: scheduler startup check failed: {e}");
        }

        loop {
            tokio::time::sleep(self.check_interval).await;

            if let Err(e) = self.check_and_notify().await {
                eprintln!("oasis: scheduler error: {e}");
            }
        }
    }

    /// Re-read the owner's chat_id from the config table.
    /// Returns 0 if not yet registered.
    async fn read_chat_id(&self) -> i64 {
        let conn = match self.conn() {
            Ok(c) => c,
            Err(_) => return 0,
        };
        let mut rows = match conn
            .query("SELECT value FROM config WHERE key = 'owner_user_id'", ())
            .await
        {
            Ok(r) => r,
            Err(_) => return 0,
        };
        match rows.next().await {
            Ok(Some(row)) => row
                .get::<String>(0)
                .ok()
                .and_then(|s| s.parse::<i64>().ok())
                .unwrap_or(0),
            _ => 0,
        }
    }

    /// Check all tasks with due dates and send appropriate notifications.
    async fn check_and_notify(&self) -> Result<()> {
        // Re-read chat_id each cycle in case owner registered after startup
        let chat_id = if self.chat_id != 0 {
            self.chat_id
        } else {
            self.read_chat_id().await
        };
        if chat_id == 0 {
            return Ok(()); // No owner yet, skip
        }

        let now = now_unix();

        // Get all non-done tasks with due dates
        let tasks = self.tasks.list_tasks(None, None).await?;
        let tasks_with_due: Vec<_> = tasks
            .into_iter()
            .filter(|t| t.due_at.is_some() && t.status != "done")
            .collect();

        if tasks_with_due.is_empty() {
            return Ok(());
        }

        for task in &tasks_with_due {
            let due_at = task.due_at.unwrap();
            let time_until = due_at - now;

            // Determine which tier to fire (most specific only, not cascading).
            // was_notified() prevents duplicates for tiers already sent.
            let tiers_to_fire: Vec<&str> = if time_until < 0 {
                vec![TIER_OVERDUE]
            } else if time_until <= 300 {
                // Within 5 minutes — due now
                vec![TIER_DUE]
            } else if time_until <= 3600 {
                // 5 min – 1 hour — coming up soon
                vec![TIER_1H]
            } else if time_until <= 86400 {
                // 1 hour – 24 hours
                vec![TIER_24H]
            } else {
                continue;
            };

            for tier in tiers_to_fire {
                if !self.was_notified(&task.id, tier).await? {
                    let message = format_notification(task, tier, self.tz_offset);
                    if let Err(e) = self.bot.send_message(chat_id, &message).await {
                        eprintln!("oasis: scheduler send failed: {e}");
                        continue;
                    }
                    self.mark_notified(&task.id, tier).await?;
                }
            }
        }

        Ok(())
    }

    /// Check if a notification has already been sent for a task+tier combo.
    async fn was_notified(&self, task_id: &str, tier: &str) -> Result<bool> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT 1 FROM reminders WHERE task_id = ?1 AND tier = ?2 AND notified = 1",
                libsql::params![task_id.to_string(), tier.to_string()],
            )
            .await
            .map_err(db_err)?;

        Ok(rows.next().await.map_err(db_err)?.is_some())
    }

    /// Record that a notification was sent.
    async fn mark_notified(&self, task_id: &str, tier: &str) -> Result<()> {
        let id = new_id();
        let now = now_unix();

        self.conn()?
            .execute(
                "INSERT INTO reminders (id, task_id, tier, notified, created_at) VALUES (?1, ?2, ?3, 1, ?4)",
                libsql::params![id, task_id.to_string(), tier.to_string(), now],
            )
            .await
            .map_err(db_err)?;

        Ok(())
    }
}

/// Format a notification message based on the tier.
fn format_notification(task: &Task, tier: &str, tz_offset: i32) -> String {
    let due_str = task.due_at.map(|ts| {
        let local_ts = ts + (tz_offset as i64) * 3600;
        let days = local_ts / 86400;
        let remainder = local_ts % 86400;
        let (y, m, d) = unix_days_to_date(days);
        if remainder == 0 {
            format!("{y:04}-{m:02}-{d:02}")
        } else {
            let h = remainder / 3600;
            let min = (remainder % 3600) / 60;
            format!("{y:04}-{m:02}-{d:02} {h:02}:{min:02}")
        }
    }).unwrap_or_default();

    match tier {
        TIER_OVERDUE => format!(
            "\u{23f0} **Overdue**: {}\nWas due: {due_str}",
            task.title
        ),
        TIER_DUE => format!(
            "\u{1f514} **Due now**: {}\nDue: {due_str}",
            task.title
        ),
        TIER_1H => format!(
            "\u{1f4cb} **Coming up in ~1 hour**: {}\nDue: {due_str}",
            task.title
        ),
        TIER_24H => format!(
            "\u{1f4c5} **Due tomorrow**: {}\nDue: {due_str}",
            task.title
        ),
        _ => format!("Reminder: {}", task.title),
    }
}

/// Convert a count of days since Unix epoch to (year, month, day).
fn unix_days_to_date(days: i64) -> (i64, i64, i64) {
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m as i64, d as i64)
}
