/// Log with HH:MM:SS timestamp.
macro_rules! log {
    ($($arg:tt)*) => {{
        let secs = oasis_core::types::now_unix();
        let h = (secs % 86400) / 3600;
        let m = (secs % 3600) / 60;
        let s = secs % 60;
        eprintln!("{h:02}:{m:02}:{s:02} oasis: {}", format_args!($($arg)*));
    }};
}

pub mod agent;
pub mod brain;
pub mod service;
pub mod tool;
