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
	ReadFile(ctx context.Context, path string) (FileContent, error)

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

	// MCPCall invokes a tool on an MCP server running inside the sandbox.
	MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error)

	// EditFile performs a surgical string replacement in a file.
	// old must exist exactly once in the file. Returns an error if old is
	// not found or appears more than once.
	EditFile(ctx context.Context, req EditFileRequest) error

	// GlobFiles finds files matching a glob pattern relative to a base directory.
	GlobFiles(ctx context.Context, req GlobRequest) ([]string, error)

	// GrepFiles searches file contents for a regex pattern and returns matches
	// with file path, line number, and matching line content.
	GrepFiles(ctx context.Context, req GrepRequest) ([]GrepMatch, error)

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

// FileContent is the output of ReadFile.
type FileContent struct {
	Content string
	Path    string
}

// WriteFileRequest is the input for WriteFile.
type WriteFileRequest struct {
	Path    string // required
	Content string // required
}

// BrowserAction describes a browser interaction.
type BrowserAction struct {
	Type string // "click", "type", "scroll", "navigate", "key"
	X    int    // coordinates for click/scroll
	Y    int
	Text string // text for type, URL for navigate
	Key  string // key name for key press
}

// BrowserResult is the output of BrowserAction.
type BrowserResult struct {
	Success bool
	Message string
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
	Pattern string // glob pattern (e.g., "**/*.py")
	Path    string // base directory; empty uses working directory
}

// GrepRequest is the input for GrepFiles.
type GrepRequest struct {
	Pattern string // regex pattern
	Path    string // base directory or file path
	Glob    string // optional file filter (e.g., "*.py")
}

// GrepMatch is a single search result from GrepFiles.
type GrepMatch struct {
	Path    string // file path
	Line    int    // line number (1-indexed)
	Content string // matching line content
}

// FileDelivery persists a file from the sandbox and returns a download URL.
// Implementations decide where to store (S3, disk, GCS, etc.).
// The framework handles downloading from the sandbox; the app handles storage.
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
