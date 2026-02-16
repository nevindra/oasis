use std::fmt;

#[derive(Debug)]
pub enum OasisError {
    Telegram(String),
    Llm { provider: String, message: String },
    Embedding(String),
    Database(String),
    Ingest(String),
    Config(String),
    Http { status: u16, body: String },
    Integration(String),
}

impl fmt::Display for OasisError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Telegram(msg) => write!(f, "telegram error: {msg}"),
            Self::Llm { provider, message } => write!(f, "llm error ({provider}): {message}"),
            Self::Embedding(msg) => write!(f, "embedding error: {msg}"),
            Self::Database(msg) => write!(f, "database error: {msg}"),
            Self::Ingest(msg) => write!(f, "ingest error: {msg}"),
            Self::Config(msg) => write!(f, "config error: {msg}"),
            Self::Http { status, body } => write!(f, "http error ({status}): {body}"),
            Self::Integration(msg) => write!(f, "integration error: {msg}"),
        }
    }
}

impl std::error::Error for OasisError {}

pub type Result<T> = std::result::Result<T, OasisError>;
