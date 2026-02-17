# Getting Started

This guide walks you through setting up the Oasis reference application from scratch.

## Prerequisites

| Requirement | Purpose |
|-------------|---------|
| **Go 1.24+** | Build and run the application |
| **Telegram Bot Token** | Messaging frontend (get from [@BotFather](https://t.me/BotFather)) |
| **Google Gemini API Key** | LLM chat + embeddings (get from [Google AI Studio](https://aistudio.google.com/)) |

Optional:
- **Brave Search API Key** -- enables the `web_search` tool ([brave.com/search/api](https://brave.com/search/api/))
- **Turso account** -- for managed remote database instead of local SQLite

## 1. Clone and Build

```bash
git clone https://github.com/nevindra/oasis.git
cd oasis
go build ./cmd/oasis/
```

This produces an `oasis` binary in the current directory.

## 2. Configure Environment Variables

Create a `.env` file in the project root:

```bash
# Required
export OASIS_TELEGRAM_TOKEN="your-telegram-bot-token"
export OASIS_LLM_API_KEY="your-gemini-api-key"
export OASIS_EMBEDDING_API_KEY="your-gemini-api-key"

# Optional
export OASIS_BRAVE_API_KEY="your-brave-search-key"
export OASIS_TURSO_URL="libsql://your-db.turso.io"
export OASIS_TURSO_TOKEN="your-turso-token"
```

The LLM and embedding API keys can be the same if you use Gemini for both (which is the default).

## 3. Review Configuration

The default `oasis.toml` ships with sensible defaults:

```toml
[telegram]
allowed_user_id = 0  # 0 = auto-register first user as owner

[llm]
provider = "gemini"
model = "gemini-2.5-flash-preview-09-2025"

[embedding]
provider = "gemini"
model = "gemini-embedding-001"
dimensions = 1536

[database]
path = "oasis.db"

[brain]
context_window = 20    # messages kept in chat context
vector_top_k = 10      # results from vector search
timezone_offset = 7    # UTC+7 (WIB)
```

You typically only need to change `timezone_offset` to match your timezone. See [Configuration](configuration.md) for the full reference.

## 4. Run

```bash
source .env && ./oasis
```

Or if you prefer running directly from source:

```bash
source .env && go run ./cmd/oasis/
```

## 5. Talk to Your Bot

Open Telegram, find your bot, and send a message. The first user to message the bot is automatically registered as the owner (when `allowed_user_id = 0`).

Try these interactions:

| Message | What Happens |
|---------|-------------|
| "Hello, how are you?" | Conversational response |
| "Search the web for Go 1.24 release notes" | Uses `web_search` tool |
| "Remember that my favorite color is blue" | Saves to knowledge base via `remember` tool |
| "What's my favorite color?" | Retrieves from knowledge base + memory |
| Send a `.txt` or `.md` file | Ingests the file into the knowledge base |
| Send a URL like `https://example.com` | Ingests the page content |

## What Gets Created

After running Oasis, you'll see:

| File/Directory | Purpose |
|----------------|---------|
| `oasis.db` | SQLite database (conversations, messages, documents, chunks, memory) |
| `~/oasis-workspace/` | Sandboxed directory for shell and file tools |

## Next Steps

- [Configuration](configuration.md) -- customize LLM models, providers, and all settings
- [Architecture](architecture.md) -- understand the framework's component design
- [Extending Oasis](extending.md) -- add your own tools, providers, or frontends
- [Deployment](deployment.md) -- deploy to production with Docker
