/// Convert a count of days since Unix epoch to (year, month, day).
pub fn unix_days_to_date(days: i64) -> (i64, i64, i64) {
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

/// Convert (year, month, day) to days since Unix epoch.
pub fn date_to_unix_days(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = (y - era * 400) as u64;
    let m = month;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + day - 1;
    let doe = yoe as i64 * 365 + yoe as i64 / 4 - yoe as i64 / 100 + doy;
    era * 146097 + doe - 719468
}

/// Format the current date+time in the user's timezone.
pub fn format_now_with_tz(tz_offset: i32) -> (String, String) {
    let utc_secs = oasis_core::types::now_unix();
    let local_secs = utc_secs + (tz_offset as i64) * 3600;
    let days = local_secs / 86400;
    let remainder = local_secs % 86400;
    let (y, m, d) = unix_days_to_date(days);
    let h = remainder / 3600;
    let min = (remainder % 3600) / 60;

    let datetime = format!("{y:04}-{m:02}-{d:02}T{h:02}:{min:02}");
    let tz_label = if tz_offset >= 0 {
        format!("+{:02}:00", tz_offset)
    } else {
        format!("-{:02}:00", tz_offset.unsigned_abs())
    };

    (datetime, tz_label)
}

/// Format a unix timestamp as a human-readable date(time) string in the given timezone.
pub fn format_due(ts: i64, tz_offset: i32) -> String {
    let local_ts = ts + (tz_offset as i64) * 3600;
    let days = local_ts / 86400;
    let remainder = local_ts % 86400;
    let (y, m, d) = unix_days_to_date(days);
    if (ts + (tz_offset as i64) * 3600) % 86400 == 0 {
        format!("{y:04}-{m:02}-{d:02}")
    } else {
        let h = remainder / 3600;
        let min = (remainder % 3600) / 60;
        format!("{y:04}-{m:02}-{d:02} {h:02}:{min:02}")
    }
}
