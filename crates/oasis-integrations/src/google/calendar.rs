use std::sync::Arc;

use oasis_core::error::{OasisError, Result};
use serde::{Deserialize, Serialize};

use super::GoogleAuth;

const CALENDAR_API: &str = "https://www.googleapis.com/calendar/v3";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CalendarEvent {
    pub id: String,
    pub summary: String,
    pub description: Option<String>,
    pub start: String,
    pub end: String,
    pub location: Option<String>,
    pub html_link: Option<String>,
    pub attendees: Vec<String>,
}

pub struct CalendarClient {
    auth: Arc<GoogleAuth>,
    http: reqwest::Client,
}

impl CalendarClient {
    pub fn new(auth: Arc<GoogleAuth>) -> Self {
        Self {
            auth,
            http: reqwest::Client::new(),
        }
    }

    async fn get(&self, url: &str) -> Result<serde_json::Value> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .get(url)
            .bearer_auth(&token)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http { status, body: text });
        }

        serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("calendar json parse failed: {e}")))
    }

    async fn post(&self, url: &str, body: &serde_json::Value) -> Result<serde_json::Value> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .post(url)
            .bearer_auth(&token)
            .json(body)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http { status, body: text });
        }

        serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("calendar json parse failed: {e}")))
    }

    async fn patch(&self, url: &str, body: &serde_json::Value) -> Result<serde_json::Value> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .patch(url)
            .bearer_auth(&token)
            .json(body)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http { status, body: text });
        }

        serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("calendar json parse failed: {e}")))
    }

    async fn delete_req(&self, url: &str) -> Result<()> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .delete(url)
            .bearer_auth(&token)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("calendar delete failed: {e}")))?;

        let status = resp.status().as_u16();
        // 204 No Content is success for delete
        if status != 204 && status != 200 {
            let text = resp.text().await.unwrap_or_default();
            return Err(OasisError::Http { status, body: text });
        }
        Ok(())
    }

    /// List events between two RFC3339 timestamps.
    /// time_min/time_max format: "2026-02-16T00:00:00Z"
    pub async fn list_events(
        &self,
        time_min: &str,
        time_max: &str,
    ) -> Result<Vec<CalendarEvent>> {
        let url = format!(
            "{CALENDAR_API}/calendars/primary/events?timeMin={time_min}&timeMax={time_max}\
             &singleEvents=true&orderBy=startTime&maxResults=50"
        );
        let data = self.get(&url).await?;
        let items = data["items"].as_array().cloned().unwrap_or_default();
        Ok(items.iter().map(parse_event).collect())
    }

    /// Create a new calendar event.
    pub async fn create_event(
        &self,
        summary: &str,
        start: &str,
        end: &str,
        description: Option<&str>,
        attendees: &[&str],
    ) -> Result<CalendarEvent> {
        let url = format!("{CALENDAR_API}/calendars/primary/events");

        let start_body = if start.contains('T') {
            serde_json::json!({ "dateTime": start })
        } else {
            serde_json::json!({ "date": start })
        };
        let end_body = if end.contains('T') {
            serde_json::json!({ "dateTime": end })
        } else {
            serde_json::json!({ "date": end })
        };

        let mut body = serde_json::json!({
            "summary": summary,
            "start": start_body,
            "end": end_body,
        });

        if let Some(desc) = description {
            body["description"] = serde_json::json!(desc);
        }

        if !attendees.is_empty() {
            let att: Vec<serde_json::Value> = attendees
                .iter()
                .map(|a| serde_json::json!({ "email": a }))
                .collect();
            body["attendees"] = serde_json::json!(att);
        }

        let data = self.post(&url, &body).await?;
        Ok(parse_event(&data))
    }

    /// Update an existing calendar event.
    pub async fn update_event(
        &self,
        event_id: &str,
        summary: Option<&str>,
        start: Option<&str>,
        end: Option<&str>,
        description: Option<&str>,
    ) -> Result<CalendarEvent> {
        let url = format!("{CALENDAR_API}/calendars/primary/events/{event_id}");
        let mut body = serde_json::json!({});

        if let Some(s) = summary {
            body["summary"] = serde_json::json!(s);
        }
        if let Some(s) = start {
            body["start"] = if s.contains('T') {
                serde_json::json!({ "dateTime": s })
            } else {
                serde_json::json!({ "date": s })
            };
        }
        if let Some(e) = end {
            body["end"] = if e.contains('T') {
                serde_json::json!({ "dateTime": e })
            } else {
                serde_json::json!({ "date": e })
            };
        }
        if let Some(desc) = description {
            body["description"] = serde_json::json!(desc);
        }

        let data = self.patch(&url, &body).await?;
        Ok(parse_event(&data))
    }

    /// Delete a calendar event.
    pub async fn delete_event(&self, event_id: &str) -> Result<()> {
        let url = format!("{CALENDAR_API}/calendars/primary/events/{event_id}");
        self.delete_req(&url).await
    }
}

fn parse_event(v: &serde_json::Value) -> CalendarEvent {
    let start = v["start"]["dateTime"]
        .as_str()
        .or_else(|| v["start"]["date"].as_str())
        .unwrap_or_default()
        .to_string();
    let end = v["end"]["dateTime"]
        .as_str()
        .or_else(|| v["end"]["date"].as_str())
        .unwrap_or_default()
        .to_string();
    let attendees = v["attendees"]
        .as_array()
        .map(|arr| {
            arr.iter()
                .filter_map(|a| a["email"].as_str().map(|s| s.to_string()))
                .collect()
        })
        .unwrap_or_default();

    CalendarEvent {
        id: v["id"].as_str().unwrap_or_default().to_string(),
        summary: v["summary"].as_str().unwrap_or("(no title)").to_string(),
        description: v["description"].as_str().map(|s| s.to_string()),
        start,
        end,
        location: v["location"].as_str().map(|s| s.to_string()),
        html_link: v["htmlLink"].as_str().map(|s| s.to_string()),
        attendees,
    }
}
