use std::sync::Arc;

use oasis_core::error::{OasisError, Result};

use crate::TokenStore;

const TOKEN_URL: &str = "https://oauth2.googleapis.com/token";
const AUTH_URL: &str = "https://accounts.google.com/o/oauth2/v2/auth";

/// Scopes requested for Google Calendar + Gmail access.
const SCOPES: &str = "https://www.googleapis.com/auth/calendar \
                       https://www.googleapis.com/auth/gmail.modify";

/// Manages Google OAuth tokens (access + refresh) via a TokenStore.
pub struct GoogleAuth {
    client_id: String,
    client_secret: String,
    callback_url: String,
    store: Arc<dyn TokenStore>,
    http: reqwest::Client,
}

impl GoogleAuth {
    pub fn new(
        client_id: String,
        client_secret: String,
        callback_url: String,
        store: Arc<dyn TokenStore>,
    ) -> Self {
        Self {
            client_id,
            client_secret,
            callback_url,
            store,
            http: reqwest::Client::new(),
        }
    }

    /// Generate the OAuth authorization URL for the user to visit.
    pub fn auth_url(&self) -> String {
        format!(
            "{AUTH_URL}?client_id={}&redirect_uri={}&response_type=code&scope={}&access_type=offline&prompt=consent",
            urlencod(&self.client_id),
            urlencod(&self.callback_url),
            urlencod(SCOPES),
        )
    }

    /// Exchange an authorization code for access + refresh tokens.
    pub async fn exchange_code(&self, code: &str) -> Result<()> {
        let params = [
            ("code", code),
            ("client_id", &self.client_id),
            ("client_secret", &self.client_secret),
            ("redirect_uri", &self.callback_url),
            ("grant_type", "authorization_code"),
        ];

        let resp = self
            .http
            .post(TOKEN_URL)
            .form(&params)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("google token exchange failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("google token read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http {
                status,
                body: text,
            });
        }

        let json: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("google token parse failed: {e}")))?;

        let access_token = json["access_token"]
            .as_str()
            .ok_or_else(|| OasisError::Integration("missing access_token".to_string()))?;
        let expires_in = json["expires_in"].as_i64().unwrap_or(3600);

        self.store.set("google_access_token", access_token).await?;

        let expiry = oasis_core::types::now_unix() + expires_in;
        self.store
            .set("google_token_expiry", &expiry.to_string())
            .await?;

        if let Some(refresh) = json["refresh_token"].as_str() {
            self.store.set("google_refresh_token", refresh).await?;
        }

        Ok(())
    }

    /// Get a valid access token, refreshing if necessary.
    pub async fn access_token(&self) -> Result<String> {
        // Check if we have a non-expired access token
        if let Some(token) = self.store.get("google_access_token").await? {
            if let Some(expiry_str) = self.store.get("google_token_expiry").await? {
                if let Ok(expiry) = expiry_str.parse::<i64>() {
                    // Refresh 60 seconds before actual expiry
                    if oasis_core::types::now_unix() < expiry - 60 {
                        return Ok(token);
                    }
                }
            }
        }

        // Need to refresh
        self.refresh().await
    }

    /// Refresh the access token using the stored refresh token.
    async fn refresh(&self) -> Result<String> {
        let refresh_token = self
            .store
            .get("google_refresh_token")
            .await?
            .ok_or_else(|| {
                OasisError::Integration(
                    "no Google refresh token. Use google_connect to authorize.".to_string(),
                )
            })?;

        let params = [
            ("refresh_token", refresh_token.as_str()),
            ("client_id", &self.client_id),
            ("client_secret", &self.client_secret),
            ("grant_type", "refresh_token"),
        ];

        let resp = self
            .http
            .post(TOKEN_URL)
            .form(&params)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("google refresh failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("google refresh read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http {
                status,
                body: text,
            });
        }

        let json: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("google refresh parse failed: {e}")))?;

        let access_token = json["access_token"]
            .as_str()
            .ok_or_else(|| OasisError::Integration("missing access_token in refresh".to_string()))?
            .to_string();

        let expires_in = json["expires_in"].as_i64().unwrap_or(3600);
        let expiry = oasis_core::types::now_unix() + expires_in;

        self.store
            .set("google_access_token", &access_token)
            .await?;
        self.store
            .set("google_token_expiry", &expiry.to_string())
            .await?;

        Ok(access_token)
    }

    /// Check if Google is connected (has a refresh token).
    pub async fn is_connected(&self) -> bool {
        matches!(self.store.get("google_refresh_token").await, Ok(Some(_)))
    }
}

/// Minimal URL encoding for query parameters.
fn urlencod(s: &str) -> String {
    s.replace('%', "%25")
        .replace(' ', "%20")
        .replace('&', "%26")
        .replace('=', "%3D")
        .replace('+', "%2B")
        .replace('/', "%2F")
        .replace(':', "%3A")
        .replace('?', "%3F")
        .replace('#', "%23")
}
