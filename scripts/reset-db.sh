#!/usr/bin/env bash
# Reset all Oasis data (remote Zeabur libSQL or local SQLite).
# Usage: ./scripts/reset-db.sh

set -euo pipefail

# Load env if available
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$SCRIPT_DIR/../.env"
if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi

URL="${OASIS_TURSO_URL:-}"
LOCAL_DB="${OASIS_DB_PATH:-oasis.db}"

TABLES=(
    "DROP INDEX IF EXISTS chunks_vector_idx"
    "DROP INDEX IF EXISTS messages_vector_idx"
    "DROP TABLE IF EXISTS chunks"
    "DROP TABLE IF EXISTS messages"
    "DROP TABLE IF EXISTS conversations"
    "DROP TABLE IF EXISTS documents"
    "DROP TABLE IF EXISTS tasks"
    "DROP TABLE IF EXISTS projects"
    "DROP TABLE IF EXISTS reminders"
    "DROP TABLE IF EXISTS user_facts"
    "DROP TABLE IF EXISTS conversation_topics"
    "DROP TABLE IF EXISTS config"
)

if [ -n "$URL" ]; then
    echo "Resetting remote database: $URL"
    STMTS=""
    for sql in "${TABLES[@]}"; do
        [ -n "$STMTS" ] && STMTS="$STMTS,"
        STMTS="$STMTS{\"type\":\"execute\",\"stmt\":{\"sql\":\"$sql\"}}"
    done
    curl -sf "$URL/v2/pipeline" \
        -H 'content-type: application/json' \
        -d "{\"requests\":[$STMTS]}" > /dev/null
    echo "Done. All tables dropped."
else
    echo "Resetting local database: $LOCAL_DB"
    rm -f "$LOCAL_DB"
    echo "Done. File deleted."
fi
