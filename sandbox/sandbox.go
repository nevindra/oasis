package sandbox

import (
	"context"
	"errors"
	"io"
)

// Sandbox provides isolated access to a running container environment.
// Each method maps to a single API call to the underlying sandbox runtime.
// Implementations must be safe for concurrent use.
type Sandbox interface {
	// Shell executes a shell command and returns its output.
	Shell(ctx context.Context, req ShellRequest) (ShellResult, error)

	// ExecCode executes code in a language-specific runtime (Python, JS, Bash, etc).
	ExecCode(ctx context.Context, req CodeRequest) (CodeResult, error)

	// ReadFile reads the content of a file inside the sandbox.
	// Returns line-numbered content with offset/limit support.
	ReadFile(ctx context.Context, req ReadFileRequest) (FileContent, error)

	// WriteFile writes text content to a file inside the sandbox.
	WriteFile(ctx context.Context, req WriteFileRequest) error

	// UploadFile uploads binary data to a file path inside the sandbox.
	UploadFile(ctx context.Context, path string, data io.Reader) error

	// DownloadFile returns a reader for a file inside the sandbox.
	// The caller must close the returned reader.
	DownloadFile(ctx context.Context, path string) (io.ReadCloser, error)

	// BrowserNavigate navigates the sandbox browser to a URL.
	BrowserNavigate(ctx context.Context, url string) error

	// BrowserScreenshot captures a screenshot of the sandbox browser.
	// Returns PNG image data.
	BrowserScreenshot(ctx context.Context) ([]byte, error)

	// BrowserAction sends an interaction to the sandbox browser.
	BrowserAction(ctx context.Context, action BrowserAction) (BrowserResult, error)

	// BrowserSnapshot returns the accessibility tree of the current page.
	// Each interactive element has a Ref that can be passed to BrowserAction
	// for precise interaction without pixel coordinates.
	BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error)

	// BrowserText extracts readable text content from the current page.
	// Uses readability-style extraction by default; set Raw to true for innerText.
	BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error)

	// BrowserPDF exports the current page as a PDF document.
	// Returns raw PDF bytes.
	BrowserPDF(ctx context.Context) ([]byte, error)

	// BrowserEval executes JavaScript in the current browser tab and returns the result.
	BrowserEval(ctx context.Context, expression string) (string, error)

	// BrowserFind uses natural-language matching to find the best element ref
	// for a given query (e.g., "submit button", "email input").
	BrowserFind(ctx context.Context, query string) (BrowserFindResult, error)

	// MCPCall invokes a tool on an MCP server running inside the sandbox.
	MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error)

	// EditFile performs a surgical string replacement in a file.
	// old must exist exactly once in the file. Returns an error if old is
	// not found or appears more than once.
	EditFile(ctx context.Context, req EditFileRequest) error

	// GlobFiles finds files matching a glob pattern relative to a base directory.
	GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error)

	// GrepFiles searches file contents for a regex pattern and returns matches
	// with file path, line number, and matching line content.
	GrepFiles(ctx context.Context, req GrepRequest) (GrepResult, error)

	// Tree returns a recursive directory listing.
	Tree(ctx context.Context, req TreeRequest) (TreeResult, error)

	// HTTPFetch fetches a URL and extracts readable text content.
	HTTPFetch(ctx context.Context, req HTTPFetchRequest) (HTTPFetchResult, error)

	// WebSearch performs a web search and returns structured results.
	WebSearch(ctx context.Context, req WebSearchRequest) (WebSearchResult, error)

	// WorkspaceInfo returns environment information about the sandbox.
	WorkspaceInfo(ctx context.Context) (WorkspaceInfoResult, error)

	// Close releases resources held by this sandbox instance. Container
	// lifecycle (stop, remove) is managed by Manager, not by Close.
	Close() error
}

// ShellRequest is the input for Shell.
type ShellRequest struct {
	Command string // required
	Cwd     string // optional working directory
	Timeout int    // seconds; 0 uses sandbox default
}

// ShellResult is the output of Shell.
type ShellResult struct {
	Output   string
	ExitCode int
}

// CodeRequest is the input for ExecCode.
type CodeRequest struct {
	Language string // "python", "javascript", "bash", etc.
	Code     string // required
	Timeout  int    // seconds; 0 uses sandbox default
}

// CodeResult is the output of ExecCode.
type CodeResult struct {
	Status string // "ok" or "error"
	Stdout string
	Stderr string
}

// ReadFileRequest is the input for ReadFile.
type ReadFileRequest struct {
	Path   string // required
	Offset int    // line offset (0-based)
	Limit  int    // max lines to return; 0 uses default (2000)
}

// FileContent is the output of ReadFile.
type FileContent struct {
	Content    string
	Path       string
	TotalLines int
}

// WriteFileRequest is the input for WriteFile.
type WriteFileRequest struct {
	Path    string // required
	Content string // required
}

// BrowserAction describes a browser interaction.
type BrowserAction struct {
	Type      string // "click", "type", "scroll", "navigate", "key", "hover", "fill", "press", "select", "focus"
	Ref       string // element ref from snapshot (preferred over coordinates)
	X         int    // pixel coordinates (fallback for canvas/maps)
	Y         int
	Text      string // text for type/fill, URL for navigate
	Key       string // key name for key/press
	Direction string // scroll direction: "up", "down", "left", "right"
	Value     string // option value for select action
}

// BrowserResult is the output of BrowserAction.
type BrowserResult struct {
	Success bool
	Message string
}

// SnapshotOpts configures a browser snapshot request.
type SnapshotOpts struct {
	Filter   string // "interactive" filters to actionable elements only
	Selector string // CSS selector to scope snapshot to a subtree
	Depth    int    // traversal depth; 0 = unlimited
}

// BrowserSnapshot is the accessibility tree of the current page.
type BrowserSnapshot struct {
	URL   string         // current page URL
	Title string         // page title
	Nodes []SnapshotNode // accessibility tree nodes
}

// SnapshotNode is a single element in the accessibility tree.
type SnapshotNode struct {
	Ref  string // element reference (e.g., "e0") — use in BrowserAction.Ref
	Role string // semantic role (link, button, textbox, heading, etc.)
	Name string // accessible name / visible text
}

// BrowserFindResult is the output of BrowserFind.
type BrowserFindResult struct {
	Ref        string  `json:"best_ref"`
	Confidence string  `json:"confidence"` // "high", "medium", "low"
	Score      float64 `json:"score"`
}

// TextOpts configures a browser text extraction request.
type TextOpts struct {
	Raw      bool // true = innerText, false = readability extraction
	MaxChars int  // 0 = unlimited
}

// BrowserTextResult is the output of BrowserText.
type BrowserTextResult struct {
	URL       string
	Title     string
	Text      string
	Truncated bool
}

// MCPRequest is the input for MCPCall.
type MCPRequest struct {
	Server string         // MCP server name
	Tool   string         // tool name
	Args   map[string]any // tool arguments
}

// MCPResult is the output of MCPCall.
type MCPResult struct {
	Content string
	IsError bool
}

// EditFileRequest is the input for EditFile.
type EditFileRequest struct {
	Path string // file to edit
	Old  string // exact string to find (must be unique in file)
	New  string // replacement string
}

// GlobRequest is the input for GlobFiles.
type GlobRequest struct {
	Pattern string   // glob pattern (e.g., "**/*.py")
	Path    string   // base directory; empty uses working directory
	Exclude []string // directories to skip (default: [".git"])
	Limit   int      // max results; 0 uses default (1000)
}

// GlobResult is the output of GlobFiles.
type GlobResult struct {
	Files     []string
	Truncated bool
}

// GrepRequest is the input for GrepFiles.
type GrepRequest struct {
	Pattern string // regex pattern
	Path    string // base directory or file path
	Glob    string // optional file filter (e.g., "*.py")
	Context int    // context lines before/after each match
	Limit   int    // max results; 0 uses default (100)
}

// GrepMatch is a single search result from GrepFiles.
type GrepMatch struct {
	Path          string   // file path
	Line          int      // line number (1-indexed)
	Content       string   // matching line content
	ContextBefore []string // lines before the match
	ContextAfter  []string // lines after the match
}

// GrepResult is the output of GrepFiles.
type GrepResult struct {
	Matches   []GrepMatch
	Truncated bool
}

// TreeRequest is the input for Tree.
type TreeRequest struct {
	Path    string   // root directory
	Depth   int      // max depth; 0 uses default (3)
	Exclude []string // directories to skip
}

// TreeResult is the output of Tree.
type TreeResult struct {
	Tree  string // formatted tree string
	Files int
	Dirs  int
}

// HTTPFetchRequest is the input for HTTPFetch.
type HTTPFetchRequest struct {
	URL      string // required
	Raw      bool   // true = raw HTML, false = readability extraction
	MaxChars int    // 0 uses default (8000)
}

// HTTPFetchResult is the output of HTTPFetch.
type HTTPFetchResult struct {
	URL     string
	Title   string
	Content string
}

// WebSearchRequest is the input for WebSearch.
type WebSearchRequest struct {
	Query      string // required
	MaxResults int    // 0 uses default (10)
}

// WebSearchResult is the output of WebSearch.
type WebSearchResult struct {
	Query   string              `json:"query"`
	Results []WebSearchResultItem `json:"results"`
}

// WebSearchResultItem is a single search result.
type WebSearchResultItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// WorkspaceInfoResult is the output of WorkspaceInfo.
type WorkspaceInfoResult struct {
	OS         string          `json:"os"`
	Arch       string          `json:"arch"`
	WorkingDir string          `json:"working_dir"`
	Tools      map[string]bool `json:"tools"`
	Browser    bool            `json:"browser"`
}

// FileDelivery persists a file from the sandbox and returns a download URL.
// Implementations decide where to store (S3, disk, GCS, etc.).
//
// Deprecated: Use FilesystemMount with MountWriteOnly mode instead.
// FileDelivery remains supported for backward compatibility but new code
// should use the more general mount system. See WithMounts and the
// FilesystemMount interface in mount.go.
type FileDelivery interface {
	Deliver(ctx context.Context, name, mimeType string, size int64, data io.Reader) (url string, err error)
}

// Sentinel errors returned by Sandbox and Manager methods.
var (
	ErrNotFound     = errors.New("sandbox not found")
	ErrCapacityFull = errors.New("sandbox capacity full")
	ErrUnhealthy    = errors.New("sandbox unhealthy")
	ErrShuttingDown = errors.New("manager shutting down")
)
