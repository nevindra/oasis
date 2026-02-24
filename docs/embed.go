// Package docs embeds the Oasis framework documentation for use by the
// MCP docs server and other tools that need access to documentation at runtime.
package docs

import "embed"

// FS contains all documentation files (concepts, guides, api, configuration,
// getting-started, and ENGINEERING.md). Use embed.FS methods to read files.
//
//go:embed concepts guides api configuration getting-started ENGINEERING.md
var FS embed.FS
