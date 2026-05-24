// Package docs embeds the Oasis framework documentation for use by the
// MCP docs server and other tools that need access to documentation at runtime.
package docs

import "embed"

// FS contains all documentation files. The layout is topic-grouped: each topic
// folder holds three files (index.md = concept, api.md = reference,
// examples.md = recipes). The landing page is at index.md.
//
// Internal contributor-only docs (PHILOSOPHY.md, ENGINEERING.md, benchmarks)
// live under internal/ and are intentionally excluded from the embed.
//
//go:embed index.md getting-started agent network workflow memory rag skills tools sandbox providers observability processors store
var FS embed.FS
