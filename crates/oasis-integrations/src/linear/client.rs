use oasis_core::error::{OasisError, Result};
use serde::{Deserialize, Serialize};

const LINEAR_API: &str = "https://api.linear.app/graphql";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LinearIssue {
    pub id: String,
    pub identifier: String,
    pub title: String,
    pub description: Option<String>,
    pub priority: f64,
    pub state: Option<LinearState>,
    pub assignee: Option<LinearUser>,
    pub team: Option<LinearTeam>,
    pub url: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LinearState {
    pub id: String,
    pub name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LinearUser {
    pub id: String,
    pub name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LinearTeam {
    pub id: String,
    pub name: String,
    pub key: String,
}

pub struct LinearClient {
    api_key: String,
    http: reqwest::Client,
}

impl LinearClient {
    pub fn new(api_key: String) -> Self {
        Self {
            api_key,
            http: reqwest::Client::new(),
        }
    }

    async fn graphql(&self, query: &str, variables: serde_json::Value) -> Result<serde_json::Value> {
        let body = serde_json::json!({
            "query": query,
            "variables": variables,
        });

        let resp = self
            .http
            .post(LINEAR_API)
            .header("Authorization", &self.api_key)
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("linear request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("linear response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http {
                status,
                body: text,
            });
        }

        let json: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("linear json parse failed: {e}")))?;

        if let Some(errors) = json.get("errors") {
            return Err(OasisError::Integration(format!(
                "linear graphql errors: {errors}"
            )));
        }

        json.get("data")
            .cloned()
            .ok_or_else(|| OasisError::Integration("linear response missing 'data'".to_string()))
    }

    pub async fn me(&self) -> Result<LinearUser> {
        let data = self
            .graphql("query { viewer { id name } }", serde_json::json!({}))
            .await?;
        let viewer = &data["viewer"];
        Ok(LinearUser {
            id: viewer["id"].as_str().unwrap_or_default().to_string(),
            name: viewer["name"].as_str().unwrap_or_default().to_string(),
        })
    }

    pub async fn list_teams(&self) -> Result<Vec<LinearTeam>> {
        let data = self
            .graphql(
                "query { teams { nodes { id name key } } }",
                serde_json::json!({}),
            )
            .await?;
        let nodes = data["teams"]["nodes"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        let teams = nodes
            .into_iter()
            .map(|n| LinearTeam {
                id: n["id"].as_str().unwrap_or_default().to_string(),
                name: n["name"].as_str().unwrap_or_default().to_string(),
                key: n["key"].as_str().unwrap_or_default().to_string(),
            })
            .collect();
        Ok(teams)
    }

    pub async fn create_issue(
        &self,
        title: &str,
        description: Option<&str>,
        team_id: &str,
        assignee_id: Option<&str>,
        priority: Option<i32>,
    ) -> Result<LinearIssue> {
        let query = r#"
            mutation IssueCreate($input: IssueCreateInput!) {
                issueCreate(input: $input) {
                    success
                    issue {
                        id identifier title description priority url
                        state { id name }
                        assignee { id name }
                        team { id name key }
                    }
                }
            }
        "#;

        let mut input = serde_json::json!({
            "title": title,
            "teamId": team_id,
        });

        if let Some(desc) = description {
            input["description"] = serde_json::json!(desc);
        }
        if let Some(aid) = assignee_id {
            input["assigneeId"] = serde_json::json!(aid);
        }
        if let Some(p) = priority {
            input["priority"] = serde_json::json!(p);
        }

        let data = self
            .graphql(query, serde_json::json!({ "input": input }))
            .await?;
        parse_issue(&data["issueCreate"]["issue"])
    }

    pub async fn list_issues(
        &self,
        team_id: Option<&str>,
        status: Option<&str>,
        assignee_id: Option<&str>,
        first: Option<i32>,
    ) -> Result<Vec<LinearIssue>> {
        let mut filter_parts = Vec::new();
        if let Some(tid) = team_id {
            filter_parts.push(format!(r#"team: {{ id: {{ eq: "{tid}" }} }}"#));
        }
        if let Some(s) = status {
            filter_parts.push(format!(r#"state: {{ name: {{ eqCaseInsensitive: "{s}" }} }}"#));
        }
        if let Some(aid) = assignee_id {
            filter_parts.push(format!(r#"assignee: {{ id: {{ eq: "{aid}" }} }}"#));
        }

        let filter = if filter_parts.is_empty() {
            String::new()
        } else {
            format!("(filter: {{ {} }})", filter_parts.join(", "))
        };

        let limit = first.unwrap_or(25);
        let query = format!(
            r#"query {{
                issues{filter} {{
                    nodes {{
                        id identifier title description priority url
                        state {{ id name }}
                        assignee {{ id name }}
                        team {{ id name key }}
                    }}
                }}
            }}"#,
            filter = filter,
        );

        // Linear doesn't support `first` in the same way; use the query as-is
        // and truncate client-side
        let data = self
            .graphql(&query, serde_json::json!({}))
            .await?;
        let nodes = data["issues"]["nodes"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        let issues: Vec<LinearIssue> = nodes
            .into_iter()
            .take(limit as usize)
            .filter_map(|n| parse_issue(&n).ok())
            .collect();
        Ok(issues)
    }

    pub async fn update_issue(
        &self,
        issue_id: &str,
        state_id: Option<&str>,
        assignee_id: Option<&str>,
        priority: Option<i32>,
    ) -> Result<LinearIssue> {
        let query = r#"
            mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
                issueUpdate(id: $id, input: $input) {
                    success
                    issue {
                        id identifier title description priority url
                        state { id name }
                        assignee { id name }
                        team { id name key }
                    }
                }
            }
        "#;

        let mut input = serde_json::json!({});
        if let Some(sid) = state_id {
            input["stateId"] = serde_json::json!(sid);
        }
        if let Some(aid) = assignee_id {
            input["assigneeId"] = serde_json::json!(aid);
        }
        if let Some(p) = priority {
            input["priority"] = serde_json::json!(p);
        }

        let data = self
            .graphql(
                query,
                serde_json::json!({ "id": issue_id, "input": input }),
            )
            .await?;
        parse_issue(&data["issueUpdate"]["issue"])
    }

    pub async fn search_issues(&self, query_text: &str) -> Result<Vec<LinearIssue>> {
        let query = r#"
            query IssueSearch($query: String!) {
                issueSearch(query: $query) {
                    nodes {
                        id identifier title description priority url
                        state { id name }
                        assignee { id name }
                        team { id name key }
                    }
                }
            }
        "#;

        let data = self
            .graphql(query, serde_json::json!({ "query": query_text }))
            .await?;
        let nodes = data["issueSearch"]["nodes"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        let issues = nodes
            .into_iter()
            .filter_map(|n| parse_issue(&n).ok())
            .collect();
        Ok(issues)
    }
}

fn parse_issue(v: &serde_json::Value) -> Result<LinearIssue> {
    Ok(LinearIssue {
        id: v["id"].as_str().unwrap_or_default().to_string(),
        identifier: v["identifier"].as_str().unwrap_or_default().to_string(),
        title: v["title"].as_str().unwrap_or_default().to_string(),
        description: v["description"].as_str().map(|s| s.to_string()),
        priority: v["priority"].as_f64().unwrap_or(0.0),
        state: v.get("state").and_then(|s| {
            if s.is_null() {
                None
            } else {
                Some(LinearState {
                    id: s["id"].as_str().unwrap_or_default().to_string(),
                    name: s["name"].as_str().unwrap_or_default().to_string(),
                })
            }
        }),
        assignee: v.get("assignee").and_then(|a| {
            if a.is_null() {
                None
            } else {
                Some(LinearUser {
                    id: a["id"].as_str().unwrap_or_default().to_string(),
                    name: a["name"].as_str().unwrap_or_default().to_string(),
                })
            }
        }),
        team: v.get("team").and_then(|t| {
            if t.is_null() {
                None
            } else {
                Some(LinearTeam {
                    id: t["id"].as_str().unwrap_or_default().to_string(),
                    name: t["name"].as_str().unwrap_or_default().to_string(),
                    key: t["key"].as_str().unwrap_or_default().to_string(),
                })
            }
        }),
        url: v["url"].as_str().unwrap_or_default().to_string(),
    })
}
