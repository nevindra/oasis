use libsql::{Connection, Database};
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;

pub struct TaskManager {
    db: Database,
}

fn db_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
}

/// Read a nullable TEXT column as Option<String>.
fn get_optional_string(row: &libsql::Row, idx: i32) -> Result<Option<String>> {
    let val = row.get::<libsql::Value>(idx).map_err(db_err)?;
    match val {
        libsql::Value::Null => Ok(None),
        libsql::Value::Text(s) => Ok(Some(s)),
        other => Err(OasisError::Database(format!(
            "expected text or null at column {idx}, got: {other:?}"
        ))),
    }
}

/// Read a nullable INTEGER column as Option<i64>.
fn get_optional_i64(row: &libsql::Row, idx: i32) -> Result<Option<i64>> {
    let val = row.get::<libsql::Value>(idx).map_err(db_err)?;
    match val {
        libsql::Value::Null => Ok(None),
        libsql::Value::Integer(i) => Ok(Some(i)),
        other => Err(OasisError::Database(format!(
            "expected integer or null at column {idx}, got: {other:?}"
        ))),
    }
}

impl TaskManager {
    pub fn new(db: Database) -> Self {
        Self { db }
    }

    /// Get a fresh database connection.
    fn conn(&self) -> Result<Connection> {
        self.db.connect().map_err(db_err)
    }

    /// Create a new project.
    pub async fn create_project(&self, name: &str, description: Option<&str>) -> Result<Project> {
        let id = new_id();
        let now = now_unix();
        let status = "active".to_string();

        self.conn()?
            .execute(
                "INSERT INTO projects (id, name, description, status, created_at, updated_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                libsql::params![
                    id.clone(),
                    name.to_string(),
                    description.map(|s| s.to_string()),
                    status.clone(),
                    now,
                    now
                ],
            )
            .await
            .map_err(db_err)?;

        Ok(Project {
            id,
            name: name.to_string(),
            description: description.map(|s| s.to_string()),
            status,
            created_at: now,
            updated_at: now,
        })
    }

    /// List projects, optionally filtered by status.
    pub async fn list_projects(&self, status: Option<&str>) -> Result<Vec<Project>> {
        let mut projects = Vec::new();
        let conn = self.conn()?;

        let mut rows = if let Some(status) = status {
            conn.query(
                "SELECT id, name, description, status, created_at, updated_at FROM projects WHERE status = ?1 ORDER BY created_at DESC",
                libsql::params![status.to_string()],
            )
            .await
            .map_err(db_err)?
        } else {
            conn.query(
                "SELECT id, name, description, status, created_at, updated_at FROM projects ORDER BY created_at DESC",
                (),
            )
            .await
            .map_err(db_err)?
        };

        while let Some(row) = rows.next().await.map_err(db_err)? {
            projects.push(Project {
                id: row.get::<String>(0).map_err(db_err)?,
                name: row.get::<String>(1).map_err(db_err)?,
                description: get_optional_string(&row, 2)?,
                status: row.get::<String>(3).map_err(db_err)?,
                created_at: row.get::<i64>(4).map_err(db_err)?,
                updated_at: row.get::<i64>(5).map_err(db_err)?,
            });
        }

        Ok(projects)
    }

    /// Create a new task.
    pub async fn create_task(
        &self,
        title: &str,
        project_id: Option<&str>,
        parent_task_id: Option<&str>,
        description: Option<&str>,
        priority: i32,
        due_at: Option<i64>,
    ) -> Result<Task> {
        let id = new_id();
        let now = now_unix();
        let status = "todo".to_string();

        self.conn()?
            .execute(
                "INSERT INTO tasks (id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
                libsql::params![
                    id.clone(),
                    project_id.map(|s| s.to_string()),
                    parent_task_id.map(|s| s.to_string()),
                    title.to_string(),
                    description.map(|s| s.to_string()),
                    status.clone(),
                    priority,
                    due_at,
                    now,
                    now
                ],
            )
            .await
            .map_err(db_err)?;

        Ok(Task {
            id,
            project_id: project_id.map(|s| s.to_string()),
            parent_task_id: parent_task_id.map(|s| s.to_string()),
            title: title.to_string(),
            description: description.map(|s| s.to_string()),
            status,
            priority,
            due_at,
            created_at: now,
            updated_at: now,
        })
    }

    /// Update a task's status. Valid statuses: "todo", "in_progress", "done".
    pub async fn update_task_status(&self, task_id: &str, status: &str) -> Result<()> {
        match status {
            "todo" | "in_progress" | "done" => {}
            _ => {
                return Err(OasisError::Database(format!(
                    "invalid task status: '{status}'. Must be one of: todo, in_progress, done"
                )));
            }
        }

        let now = now_unix();

        let affected = self
            .conn()?
            .execute(
                "UPDATE tasks SET status = ?1, updated_at = ?2 WHERE id = ?3",
                libsql::params![status.to_string(), now, task_id.to_string()],
            )
            .await
            .map_err(db_err)?;

        if affected == 0 {
            return Err(OasisError::Database(format!(
                "task not found: {task_id}"
            )));
        }

        Ok(())
    }

    /// List tasks, optionally filtered by project and/or status.
    pub async fn list_tasks(
        &self,
        project_id: Option<&str>,
        status: Option<&str>,
    ) -> Result<Vec<Task>> {
        let mut conditions = Vec::new();
        let mut sql = String::from(
            "SELECT id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at FROM tasks",
        );

        if project_id.is_some() {
            conditions.push("project_id = ?");
        }
        if status.is_some() {
            conditions.push("status = ?");
        }

        if !conditions.is_empty() {
            sql.push_str(" WHERE ");
            sql.push_str(&conditions.join(" AND "));
        }

        sql.push_str(" ORDER BY priority DESC, created_at ASC");

        let conn = self.conn()?;

        // Build params dynamically based on which filters are present
        let mut rows = match (project_id, status) {
            (Some(pid), Some(st)) => {
                conn.query(&sql, libsql::params![pid.to_string(), st.to_string()])
                    .await
                    .map_err(db_err)?
            }
            (Some(pid), None) => {
                conn.query(&sql, libsql::params![pid.to_string()])
                    .await
                    .map_err(db_err)?
            }
            (None, Some(st)) => {
                conn.query(&sql, libsql::params![st.to_string()])
                    .await
                    .map_err(db_err)?
            }
            (None, None) => conn.query(&sql, ()).await.map_err(db_err)?,
        };

        let mut tasks = Vec::new();
        while let Some(row) = rows.next().await.map_err(db_err)? {
            tasks.push(row_to_task(&row)?);
        }

        Ok(tasks)
    }

    /// Get a single task by ID.
    pub async fn get_task(&self, task_id: &str) -> Result<Option<Task>> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at FROM tasks WHERE id = ?1",
                libsql::params![task_id.to_string()],
            )
            .await
            .map_err(db_err)?;

        match rows.next().await.map_err(db_err)? {
            Some(row) => Ok(Some(row_to_task(&row)?)),
            None => Ok(None),
        }
    }

    /// Find tasks whose title matches a substring (case-insensitive LIKE).
    pub async fn find_task_by_title(&self, title_query: &str) -> Result<Vec<Task>> {
        let pattern = format!("%{title_query}%");

        let mut rows = self
            .conn()?
            .query(
                "SELECT id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at FROM tasks WHERE title LIKE ?1",
                libsql::params![pattern],
            )
            .await
            .map_err(db_err)?;

        let mut tasks = Vec::new();
        while let Some(row) = rows.next().await.map_err(db_err)? {
            tasks.push(row_to_task(&row)?);
        }

        Ok(tasks)
    }

    /// Find tasks whose ID ends with the given short hex string.
    pub async fn find_task_by_short_id(&self, short_id: &str) -> Result<Vec<Task>> {
        let pattern = format!("%{short_id}");

        let mut rows = self
            .conn()?
            .query(
                "SELECT id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at FROM tasks WHERE id LIKE ?1",
                libsql::params![pattern],
            )
            .await
            .map_err(db_err)?;

        let mut tasks = Vec::new();
        while let Some(row) = rows.next().await.map_err(db_err)? {
            tasks.push(row_to_task(&row)?);
        }

        Ok(tasks)
    }

    /// Build a formatted summary of all active (non-done) tasks, grouped by project.
    /// Used to inject into the system prompt.
    pub async fn get_active_task_summary(&self, tz_offset: i32) -> Result<String> {
        // Fetch all non-done tasks with their project name (if any)
        let mut rows = self
            .conn()?
            .query(
                "SELECT t.id, t.project_id, t.parent_task_id, t.title, t.description, \
                        t.status, t.priority, t.due_at, t.created_at, t.updated_at, p.name \
                 FROM tasks t \
                 LEFT JOIN projects p ON t.project_id = p.id \
                 WHERE t.status != 'done' \
                 ORDER BY p.name ASC, t.priority DESC, t.created_at ASC",
                (),
            )
            .await
            .map_err(db_err)?;

        // Group tasks by project name. We rely on ORDER BY to keep groups contiguous.
        // None project_name = unassigned tasks (project_id is NULL or project not found).
        let mut project_tasks: Vec<(Option<String>, Vec<Task>)> = Vec::new();
        let mut current_project: Option<Option<String>> = None;

        while let Some(row) = rows.next().await.map_err(db_err)? {
            let task = row_to_task(&row)?;
            let project_name = get_optional_string(&row, 10)?;

            match &current_project {
                Some(cp) if cp == &project_name => {
                    // Same project group -- append to last entry
                    project_tasks.last_mut().unwrap().1.push(task);
                }
                _ => {
                    // New project group
                    current_project = Some(project_name.clone());
                    project_tasks.push((project_name, vec![task]));
                }
            }
        }

        if project_tasks.is_empty() {
            return Ok(String::from("No active tasks."));
        }

        let mut output = String::new();
        for (project_name, tasks) in &project_tasks {
            match project_name {
                Some(name) => {
                    output.push_str(&format!("Project: {name}\n"));
                }
                None => {
                    output.push_str("Unassigned:\n");
                }
            }

            for task in tasks {
                let due_str = match task.due_at {
                    Some(ts) => {
                        let local_ts = ts + (tz_offset as i64) * 3600;
                        let days = local_ts / 86400;
                        let remainder = local_ts % 86400;
                        let (y, m, d) = unix_days_to_date(days);
                        if remainder == 0 {
                            format!(" (due: {y:04}-{m:02}-{d:02})")
                        } else {
                            let h = remainder / 3600;
                            let min = (remainder % 3600) / 60;
                            format!(" (due: {y:04}-{m:02}-{d:02} {h:02}:{min:02})")
                        }
                    }
                    None => String::new(),
                };
                let short = &task.id[task.id.len().saturating_sub(6)..];
                output.push_str(&format!("  - [{}] #{} {}{}\n", task.status, short, task.title, due_str));
            }

            output.push('\n');
        }

        // Remove trailing newline
        let trimmed = output.trim_end().to_string();
        Ok(trimmed)
    }

    /// Delete all tasks. Returns the number of deleted tasks.
    pub async fn delete_all_tasks(&self) -> Result<u64> {
        let affected = self
            .conn()?
            .execute("DELETE FROM tasks", ())
            .await
            .map_err(db_err)?;
        Ok(affected)
    }

    /// Delete a task by ID.
    pub async fn delete_task(&self, task_id: &str) -> Result<()> {
        let affected = self
            .conn()?
            .execute(
                "DELETE FROM tasks WHERE id = ?1",
                libsql::params![task_id.to_string()],
            )
            .await
            .map_err(db_err)?;

        if affected == 0 {
            return Err(OasisError::Database(format!(
                "task not found: {task_id}"
            )));
        }

        Ok(())
    }
}

/// Extract a Task from a libsql Row. Expects columns in the standard order:
/// id, project_id, parent_task_id, title, description, status, priority, due_at, created_at, updated_at
fn row_to_task(row: &libsql::Row) -> Result<Task> {
    Ok(Task {
        id: row.get::<String>(0).map_err(db_err)?,
        project_id: get_optional_string(row, 1)?,
        parent_task_id: get_optional_string(row, 2)?,
        title: row.get::<String>(3).map_err(db_err)?,
        description: get_optional_string(row, 4)?,
        status: row.get::<String>(5).map_err(db_err)?,
        priority: row.get::<i64>(6).map_err(db_err)? as i32,
        due_at: get_optional_i64(row, 7)?,
        created_at: row.get::<i64>(8).map_err(db_err)?,
        updated_at: row.get::<i64>(9).map_err(db_err)?,
    })
}

/// Convert a count of days since Unix epoch to (year, month, day).
fn unix_days_to_date(days: i64) -> (i64, i64, i64) {
    // Algorithm adapted from Howard Hinnant's civil_from_days
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64; // day of era [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365; // year of era [0, 399]
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // day of year [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let d = doy - (153 * mp + 2) / 5 + 1; // [1, 31]
    let m = if mp < 10 { mp + 3 } else { mp - 9 }; // [1, 12]
    let y = if m <= 2 { y + 1 } else { y };
    (y, m as i64, d as i64)
}
