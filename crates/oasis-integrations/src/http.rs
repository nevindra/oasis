use std::sync::Arc;

use axum::extract::{Query, State};
use axum::response::Html;
use axum::routing::get;
use axum::Router;
use oasis_core::error::{OasisError, Result};

use crate::google::GoogleAuth;

#[derive(serde::Deserialize)]
pub struct OAuthCallback {
    code: Option<String>,
    error: Option<String>,
}

struct AppState {
    auth: Arc<GoogleAuth>,
}

async fn oauth_callback(
    State(state): State<Arc<AppState>>,
    Query(params): Query<OAuthCallback>,
) -> Html<String> {
    if let Some(error) = params.error {
        return Html(format!(
            "<h1>Authorization failed</h1><p>{error}</p>"
        ));
    }

    let code = match params.code {
        Some(c) => c,
        None => {
            return Html(
                "<h1>Error</h1><p>No authorization code received.</p>".to_string(),
            )
        }
    };

    match state.auth.exchange_code(&code).await {
        Ok(()) => Html(
            "<h1>Connected!</h1><p>Google account connected to Oasis. You can close this tab.</p>"
                .to_string(),
        ),
        Err(e) => Html(format!(
            "<h1>Error</h1><p>Failed to connect: {e}</p>"
        )),
    }
}

/// Start the OAuth callback HTTP server.
/// Runs until the provided shutdown signal completes.
pub async fn start_oauth_server(port: u16, auth: Arc<GoogleAuth>) -> Result<()> {
    let state = Arc::new(AppState { auth });

    let app = Router::new()
        .route("/oauth/callback", get(oauth_callback))
        .with_state(state);

    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}"))
        .await
        .map_err(|e| OasisError::Integration(format!("failed to bind port {port}: {e}")))?;

    axum::serve(listener, app)
        .await
        .map_err(|e| OasisError::Integration(format!("oauth server error: {e}")))?;

    Ok(())
}
