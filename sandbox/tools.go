package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nevindra/oasis/core"
	oasis "github.com/nevindra/oasis/core"
)

// toolImpl wraps a single tool definition and its execute function.
type toolImpl struct {
	def     oasis.ToolDefinition
	execute func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error)
}

func newTool(name, description, schema string, exec func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error)) toolImpl {
	return toolImpl{
		def: oasis.ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  json.RawMessage(schema),
		},
		execute: exec,
	}
}

func (t toolImpl) Name() string                     { return t.def.Name }
func (t toolImpl) Definition() oasis.ToolDefinition { return t.def }

func (t toolImpl) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	return t.execute(ctx, args)
}

// ToolsOption configures optional sandbox tool capabilities.
type ToolsOption func(*toolsConfig)

type toolsConfig struct {
	delivery  FileDelivery
	mounts    []MountSpec
	manifest  *Manifest
	noBrowser bool
}

// WithFileDelivery enables the deliver_file tool with a single legacy
// FileDelivery destination.
//
// Deprecated: Use WithMounts with a MountWriteOnly MountSpec instead.
// This option remains for backward compatibility and is honored as a
// fallback inside deliver_file when no mount covers the requested path.
func WithFileDelivery(fd FileDelivery) ToolsOption {
	return func(c *toolsConfig) { c.delivery = fd }
}

// WithoutBrowser omits the browser tool set (browser, screenshot, snapshot,
// page_text, export_pdf, browser_eval, browser_find, browser_wait) from the
// returned tools. Use for "light" sandboxes that have no browser capability,
// so the model is never offered tools that would fail.
func WithoutBrowser() ToolsOption {
	return func(c *toolsConfig) { c.noBrowser = true }
}

// WithMounts attaches a slice of FilesystemMount specs to the tool layer.
// Tool wrappers consult the mounts to publish writes back to the backend
// and to look up version preconditions in the supplied manifest.
//
// The manifest is shared with PrefetchMounts/FlushMounts so that all three
// layers see the same per-sandbox version state.
func WithMounts(specs []MountSpec, manifest *Manifest) ToolsOption {
	return func(c *toolsConfig) {
		c.mounts = specs
		c.manifest = manifest
	}
}

// findMountForPath returns the deepest matching mount for an absolute
// sandbox path, or (nil, "") if no mount covers the path. The deepest
// match wins so that a nested mount takes precedence over a parent mount.
// The second return value is the path's logical key relative to the
// matched mount root.
func findMountForPath(mounts []MountSpec, p string) (*MountSpec, string) {
	var best *MountSpec
	bestLen := -1
	var bestKey string
	for i := range mounts {
		m := &mounts[i]
		prefix := m.Path
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		// Avoid matching "/workspace/inputs2" when mount is "/workspace/inputs":
		// the path must either equal the prefix or have "/" right after it.
		if p != prefix && !strings.HasPrefix(p[len(prefix):], "/") {
			continue
		}
		if len(prefix) > bestLen {
			best = m
			bestLen = len(prefix)
			rel := strings.TrimPrefix(p, prefix)
			bestKey = strings.TrimPrefix(rel, "/")
		}
	}
	return best, bestKey
}

// --- file-scope arg types for tool schema derivation ---

type shellArgs struct {
	Command string `json:"command" describe:"Shell command to execute"`
	Cwd     string `json:"cwd,omitempty" describe:"Working directory"`
}

type executeCodeArgs struct {
	Code     string `json:"code" describe:"Code to execute"`
	Language string `json:"language,omitempty" describe:"Language runtime (default: python)"`
}

type fileReadArgs struct {
	Path   string `json:"path" describe:"File path to read"`
	Offset int    `json:"offset,omitempty" describe:"Line offset to start reading from (0-based, default 0)"`
	Limit  int    `json:"limit,omitempty" describe:"Maximum number of lines to read (default 2000)"`
}

type fileWriteArgs struct {
	Path    string `json:"path" describe:"File path to write"`
	Content string `json:"content" describe:"File content"`
}

type fileEditArgs struct {
	Path       string `json:"path" describe:"Path to the file to edit"`
	OldString  string `json:"old_string" describe:"Exact text to find. Must be unique in the file unless replace_all is true; include surrounding lines to disambiguate"`
	NewString  string `json:"new_string" describe:"Replacement text. Empty string deletes the matched text"`
	ReplaceAll bool   `json:"replace_all,omitempty" describe:"Replace every occurrence instead of requiring a unique match (default: false)"`
}

type fileSearchArgs struct {
	Target  string   `json:"target,omitempty" describe:"'content' (default) = regex search inside files, 'files' = find files by glob, 'tree' = directory tree"`
	Pattern string   `json:"pattern,omitempty" describe:"Regex (content) or glob like '**/*.py' (files). Required except for tree"`
	Path    string   `json:"path,omitempty" describe:"Directory (or file, for content) to search in (default: working directory)"`
	Glob    string   `json:"glob,omitempty" describe:"content only: restrict search to files matching this glob (e.g. '*.py')"`
	Context int      `json:"context,omitempty" describe:"content only: context lines around each match (default: 0)"`
	Depth   int      `json:"depth,omitempty" describe:"tree only: maximum depth to traverse (default: 3)"`
	Exclude []string `json:"exclude,omitempty" describe:"files/tree: directory names to skip (default: .git, node_modules, ...)"`
	Limit   int      `json:"limit,omitempty" describe:"Maximum results (default: 100 matches / 1000 files)"`
}

type emptyArgs struct{}

type browserArgs struct {
	Action    string `json:"action" describe:"Browser action: navigate, click, type, fill, scroll, key, hover, press, select, focus"`
	Ref       string `json:"ref,omitempty" describe:"Element reference from snapshot (e.g., 'e5'). REQUIRED for click, type, fill, hover, focus, select actions."`
	URL       string `json:"url,omitempty" describe:"URL for navigate action"`
	X         int    `json:"x,omitempty" describe:"X coordinate (fallback when ref not available)"`
	Y         int    `json:"y,omitempty" describe:"Y coordinate (fallback when ref not available)"`
	Text      string `json:"text,omitempty" describe:"Text to type or fill into the element specified by ref"`
	Key       string `json:"key,omitempty" describe:"Key to press (e.g., 'Enter', 'Tab', 'Escape')"`
	Direction string `json:"direction,omitempty" describe:"Scroll direction: up, down, left, right"`
	Value     string `json:"value,omitempty" describe:"Option value for select action"`
}

type snapshotArgs struct {
	Filter   string `json:"filter,omitempty" describe:"Set to 'interactive' to show only actionable elements"`
	Selector string `json:"selector,omitempty" describe:"CSS selector to scope snapshot to a subtree"`
	Depth    int    `json:"depth,omitempty" describe:"Tree traversal depth limit (0 = unlimited)"`
}

type pageTextArgs struct {
	Raw      bool `json:"raw,omitempty" describe:"true = raw innerText, false = readability extraction (default)"`
	MaxChars int  `json:"max_chars,omitempty" describe:"Truncation limit in characters (0 = unlimited)"`
}

type browserEvalArgs struct {
	Expression string `json:"expression" describe:"JavaScript expression to evaluate (e.g., 'document.title', 'document.querySelector(\"input\").value')"`
}

type browserFindArgs struct {
	Query string `json:"query" describe:"Natural-language description of the element (e.g., 'submit button', 'email input', 'search box')"`
}

type browserWaitArgs struct {
	Kind      string `json:"kind" describe:"What to wait for: selector, text, url, load, time, function"`
	Value     string `json:"value,omitempty" describe:"Depends on kind — CSS selector (selector), text to appear (text), URL glob (url), load state: ready-state|content-loaded|network-idle (load), JS expression (function). Unused for time."`
	TimeoutMs int    `json:"timeout_ms,omitempty" describe:"Max wait in milliseconds (default 10000, max 30000). For kind=time this is the delay itself."`
	State     string `json:"state,omitempty" describe:"selector kind only: visible (default) or hidden"`
}

type httpFetchArgs struct {
	URL      string `json:"url" describe:"URL to fetch"`
	Raw      bool   `json:"raw,omitempty" describe:"true = raw HTML, false = readability extraction (default)"`
	MaxChars int    `json:"max_chars,omitempty" describe:"Truncation limit in characters (default: 8000)"`
}

type webSearchArgs struct {
	Query      string `json:"query" describe:"Search query"`
	MaxResults int    `json:"max_results,omitempty" describe:"Maximum number of results (default: 10)"`
}

type mcpCallArgs struct {
	Server string          `json:"server" describe:"MCP server name"`
	Tool   string          `json:"tool" describe:"Tool name"`
	Args   json.RawMessage `json:"args,omitempty" describe:"Tool arguments"`
}

type deliverFileArgs struct {
	Path string `json:"path" describe:"Absolute path to the file in the sandbox"`
	Name string `json:"name,omitempty" describe:"Display name for the download. Defaults to the filename."`
}

// Tools returns Oasis tool implementations backed by the given Sandbox.
func Tools(sb Sandbox, opts ...ToolsOption) []oasis.AnyTool {
	cfg := &toolsConfig{}
	for _, o := range opts {
		o(cfg)
	}

	tools := []oasis.AnyTool{
		shellTool(sb),
		executeCodeTool(sb),
		fileReadTool(sb),
		fileWriteTool(sb, cfg),
		fileEditTool(sb, cfg),
		fileSearchTool(sb),
		httpFetchTool(sb),
		workspaceInfoTool(sb),
		mcpCallTool(sb),
		webSearchTool(sb),
	}

	// Browser tools are registered only when the sandbox actually drives a
	// browser (satisfies the optional BrowserSandbox interface) and the
	// caller has not opted out via WithoutBrowser. Mirrors the
	// FilesystemMounter assertion pattern in mounter.go.
	if b, ok := sb.(BrowserSandbox); ok && !cfg.noBrowser {
		tools = append(tools,
			browserTool(b),
			screenshotTool(b),
			snapshotTool(b),
			pageTextTool(b),
			exportPDFTool(b),
			browserEvalTool(b),
			browserFindTool(b),
			browserWaitTool(b),
		)
	}

	// Register deliver_file when ANY destination is available — either an
	// explicit FileDelivery (legacy) or at least one writeable mount.
	if cfg.delivery != nil || hasWriteableMount(cfg.mounts) {
		tools = append(tools, deliverFileTool(sb, cfg))
	}

	return tools
}

func hasWriteableMount(mounts []MountSpec) bool {
	for _, m := range mounts {
		if m.Mode.Writable() {
			return true
		}
	}
	return false
}

func shellTool(sb Sandbox) toolImpl {
	return newTool("shell",
		"Execute a shell command in the sandbox. Use for builds, git, installing packages, and anything without a dedicated tool. Do NOT use it for file work — use file_read, file_write, file_edit, and file_search instead — or for fetching URLs (use http_fetch).",
		string(core.DeriveSchema[shellArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p shellArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			res, err := sb.Shell(ctx, ShellRequest{Command: p.Command, Cwd: p.Cwd})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			output := res.Output
			if res.ExitCode != 0 {
				output = fmt.Sprintf("exit code %d\n%s", res.ExitCode, output)
			}
			return oasis.TextResult(output), nil
		})
}

func executeCodeTool(sb Sandbox) toolImpl {
	return newTool("execute_code",
		"Execute code in a language runtime",
		string(core.DeriveSchema[executeCodeArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p executeCodeArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			if p.Language == "" {
				p.Language = "python"
			}
			res, err := sb.ExecCode(ctx, CodeRequest{Code: p.Code, Language: p.Language})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if res.Status != "ok" {
				errMsg := res.Stderr
				if errMsg == "" {
					errMsg = res.Stdout
				}
				return oasis.ToolResult{Error: errMsg}, nil
			}
			output := res.Stdout
			if res.Stderr != "" {
				output += "\nstderr: " + res.Stderr
			}
			return oasis.TextResult(output), nil
		})
}

func fileReadTool(sb Sandbox) toolImpl {
	return newTool("file_read",
		"Read a text file with line numbers (cat -n format). Use offset/limit to page through large files. Use this instead of cat/head/tail via shell.",
		string(core.DeriveSchema[fileReadArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p fileReadArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			fc, err := sb.ReadFile(ctx, ReadFileRequest{Path: p.Path, Offset: p.Offset, Limit: p.Limit})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(fc.Content), nil
		})
}

func fileWriteTool(sb Sandbox, cfg *toolsConfig) toolImpl {
	return newTool("file_write",
		"Write content to a file, replacing anything there; parent directories are created automatically. Use file_edit for targeted edits. Use this instead of echo/heredoc via shell.",
		string(core.DeriveSchema[fileWriteArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p fileWriteArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			if err := sb.WriteFile(ctx, WriteFileRequest{Path: p.Path, Content: p.Content}); err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if err := publishToMount(ctx, cfg, p.Path, []byte(p.Content)); err != nil {
				return oasis.ToolResult{Error: "wrote locally but publish failed: " + err.Error()}, nil
			}
			return oasis.TextResult("wrote to " + p.Path), nil
		})
}

// publishToMount writes content to whichever mount covers path, if any.
// Returns nil for paths that fall under no mount, or under read-only
// mounts (which silently absorb the local write without persisting).
// Returns an error from the backend if the publish fails or conflicts.
func publishToMount(ctx context.Context, cfg *toolsConfig, p string, content []byte) error {
	if cfg == nil || len(cfg.mounts) == 0 {
		return nil
	}
	mount, key := findMountForPath(cfg.mounts, p)
	if mount == nil || !mount.Mode.Writable() {
		return nil
	}
	ver := ""
	if cfg.manifest != nil {
		ver, _ = cfg.manifest.Version(mount.Path, key)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(p))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	newVer, err := mount.Backend.Put(ctx, key, mimeType, int64(len(content)), bytes.NewReader(content), ver)
	if err != nil {
		return err
	}
	if cfg.manifest != nil {
		cfg.manifest.Record(mount.Path, key, MountEntry{
			Key:      key,
			Size:     int64(len(content)),
			MimeType: mimeType,
			Version:  newVer,
		})
	}
	return nil
}

// maxEditFileBytes caps the file size for file_edit's read-modify-write
// cycle so a stray edit on a huge file cannot exhaust memory.
const maxEditFileBytes = 10 * 1024 * 1024 // 10 MB

func fileEditTool(sb Sandbox, cfg *toolsConfig) toolImpl {
	return newTool("file_edit",
		"Replace an exact string in a file. old_string must match exactly once — include surrounding lines to disambiguate — or set replace_all=true to change every occurrence. Returns a unified diff of the change. Use this instead of sed/awk via shell.",
		string(core.DeriveSchema[fileEditArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p fileEditArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			if p.OldString == "" {
				return oasis.ToolResult{Error: "old_string is required"}, nil
			}
			if p.OldString == p.NewString {
				return oasis.ToolResult{Error: "old_string and new_string are identical"}, nil
			}
			rc, err := sb.DownloadFile(ctx, p.Path)
			if err != nil {
				return oasis.ToolResult{Error: "read failed: " + err.Error()}, nil
			}
			if rc == nil {
				return oasis.ToolResult{Error: "read failed: no content for " + p.Path}, nil
			}
			data, err := io.ReadAll(io.LimitReader(rc, maxEditFileBytes+1))
			rc.Close()
			if err != nil {
				return oasis.ToolResult{Error: "read failed: " + err.Error()}, nil
			}
			if len(data) > maxEditFileBytes {
				return oasis.ToolResult{Error: fmt.Sprintf("file too large to edit (max %s); use shell or execute_code instead", humanSize(maxEditFileBytes))}, nil
			}
			before := string(data)
			n := strings.Count(before, p.OldString)
			switch {
			case n == 0:
				return oasis.ToolResult{Error: fmt.Sprintf("old_string not found in %s", p.Path)}, nil
			case n > 1 && !p.ReplaceAll:
				return oasis.ToolResult{Error: fmt.Sprintf("old_string appears %d times in %s; add surrounding context to make it unique, or set replace_all=true", n, p.Path)}, nil
			}
			after := strings.ReplaceAll(before, p.OldString, p.NewString)
			if err := sb.WriteFile(ctx, WriteFileRequest{Path: p.Path, Content: after}); err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if err := publishToMount(ctx, cfg, p.Path, []byte(after)); err != nil {
				return oasis.ToolResult{Error: "edited locally but publish failed: " + err.Error()}, nil
			}
			plural := ""
			if n > 1 {
				plural = "s"
			}
			return oasis.TextResult(fmt.Sprintf("edited %s (%d replacement%s)\n%s", p.Path, n, plural, editDiff(before, p.OldString, p.NewString))), nil
		})
}

// editDiff renders unified-diff style hunks for the non-overlapping
// replacements of oldStr with newStr inside before, so the model sees what
// actually changed. Nearby replacements merge into one hunk so same-line and
// adjacent edits stay accurate.
func editDiff(before, oldStr, newStr string) string {
	const contextLines = 2
	const maxHunks = 5

	// Non-overlapping occurrence offsets, matching strings.ReplaceAll.
	var offs []int
	for i := 0; ; {
		j := strings.Index(before[i:], oldStr)
		if j < 0 {
			break
		}
		offs = append(offs, i+j)
		i += j + len(oldStr)
	}
	if len(offs) == 0 {
		return ""
	}

	lines := strings.Split(before, "\n")
	starts := make([]int, len(lines)) // byte offset where each line begins
	pos := 0
	for i, l := range lines {
		starts[i] = pos
		pos += len(l) + 1
	}
	lineOf := func(off int) int {
		return sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
	}
	lineEnd := func(i int) int { return starts[i] + len(lines[i]) }

	// Group occurrences whose context windows touch or overlap.
	type hunkGroup struct {
		offs               []int
		startLine, endLine int
	}
	var groups []hunkGroup
	for _, off := range offs {
		sl := lineOf(off)
		el := lineOf(off + len(oldStr) - 1)
		if n := len(groups); n > 0 && sl <= groups[n-1].endLine+2*contextLines+1 {
			g := &groups[n-1]
			g.offs = append(g.offs, off)
			if el > g.endLine {
				g.endLine = el
			}
		} else {
			groups = append(groups, hunkGroup{offs: []int{off}, startLine: sl, endLine: el})
		}
	}

	var b strings.Builder
	delta := 0 // cumulative line-count change on the new side
	for gi, g := range groups {
		if gi == maxHunks {
			fmt.Fprintf(&b, "... (%d more hunks)\n", len(groups)-maxHunks)
			break
		}
		// Rebuild the group's byte span with its replacements applied.
		var seg strings.Builder
		cur := starts[g.startLine]
		for _, off := range g.offs {
			seg.WriteString(before[cur:off])
			seg.WriteString(newStr)
			cur = off + len(oldStr)
		}
		seg.WriteString(before[cur:lineEnd(g.endLine)])
		newLines := strings.Split(seg.String(), "\n")

		ctxStart := max(g.startLine-contextLines, 0)
		ctxEnd := min(g.endLine+contextLines, len(lines)-1)
		oldCount := ctxEnd - ctxStart + 1
		newCount := (g.startLine - ctxStart) + len(newLines) + (ctxEnd - g.endLine)
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", ctxStart+1, oldCount, ctxStart+1+delta, newCount)
		for i := ctxStart; i < g.startLine; i++ {
			b.WriteString(" " + lines[i] + "\n")
		}
		for i := g.startLine; i <= g.endLine; i++ {
			b.WriteString("-" + lines[i] + "\n")
		}
		for _, l := range newLines {
			b.WriteString("+" + l + "\n")
		}
		for i := g.endLine + 1; i <= ctxEnd; i++ {
			b.WriteString(" " + lines[i] + "\n")
		}
		delta += len(newLines) - (g.endLine - g.startLine + 1)
	}
	return strings.TrimRight(b.String(), "\n")
}

func fileSearchTool(sb Sandbox) toolImpl {
	return newTool("file_search",
		"Search file contents, find files by name, or view directory structure. Use this instead of grep/rg/find/ls/tree via shell. target='content' (default): regex search inside files, returns path:line matches with optional context. target='files': list files matching a glob pattern. target='tree': indented directory tree with file/dir counts.",
		string(core.DeriveSchema[fileSearchArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p fileSearchArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			switch p.Target {
			case "", "content":
				if p.Pattern == "" {
					return oasis.ToolResult{Error: "pattern is required when target='content'"}, nil
				}
				res, err := sb.GrepFiles(ctx, GrepRequest{Pattern: p.Pattern, Path: p.Path, Glob: p.Glob, Context: p.Context, Limit: p.Limit})
				if err != nil {
					return oasis.ToolResult{Error: err.Error()}, nil
				}
				if len(res.Matches) == 0 {
					return oasis.TextResult("no matches found"), nil
				}
				var b strings.Builder
				for i, m := range res.Matches {
					if i > 0 {
						b.WriteString("\n")
					}
					for j, cl := range m.ContextBefore {
						fmt.Fprintf(&b, "%s:%d- %s\n", m.Path, m.Line-len(m.ContextBefore)+j, cl)
					}
					fmt.Fprintf(&b, "%s:%d: %s", m.Path, m.Line, m.Content)
					for j, cl := range m.ContextAfter {
						fmt.Fprintf(&b, "\n%s:%d- %s", m.Path, m.Line+j+1, cl)
					}
				}
				if res.Truncated {
					b.WriteString("\n... (truncated; narrow the pattern or raise limit)")
				}
				return oasis.TextResult(b.String()), nil
			case "files":
				pattern := p.Pattern
				if pattern == "" {
					// Models sometimes put the glob in the content-mode
					// filter param; accept it rather than bounce the call.
					pattern = p.Glob
				}
				if pattern == "" {
					return oasis.ToolResult{Error: "pattern is required when target='files'"}, nil
				}
				res, err := sb.GlobFiles(ctx, GlobRequest{Pattern: pattern, Path: p.Path, Exclude: p.Exclude, Limit: p.Limit})
				if err != nil {
					return oasis.ToolResult{Error: err.Error()}, nil
				}
				if len(res.Files) == 0 {
					return oasis.TextResult("no files matched"), nil
				}
				out := strings.Join(res.Files, "\n")
				if res.Truncated {
					out += "\n... (truncated)"
				}
				return oasis.TextResult(out), nil
			case "tree":
				res, err := sb.Tree(ctx, TreeRequest{Path: p.Path, Depth: p.Depth, Exclude: p.Exclude})
				if err != nil {
					return oasis.ToolResult{Error: err.Error()}, nil
				}
				return oasis.TextResult(fmt.Sprintf("%s\n\n%d files, %d directories", res.Tree, res.Files, res.Dirs)), nil
			default:
				return oasis.ToolResult{Error: fmt.Sprintf("unknown target %q: use 'content', 'files', or 'tree'", p.Target)}, nil
			}
		})
}

func httpFetchTool(sb Sandbox) toolImpl {
	return newTool("http_fetch",
		"Fetch a URL and return readable text (raw=true for unprocessed HTML). Plain HTTP GET — sites with bot protection may return 403/502; in that case use browser(action='navigate') + page_text instead.",
		string(core.DeriveSchema[httpFetchArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p httpFetchArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			res, err := sb.HTTPFetch(ctx, HTTPFetchRequest{URL: p.URL, Raw: p.Raw, MaxChars: p.MaxChars})
			if err != nil {
				errMsg := err.Error()
				if strings.Contains(errMsg, "403") || strings.Contains(errMsg, "502") || strings.Contains(errMsg, "503") ||
					strings.Contains(errMsg, "stream error") || strings.Contains(errMsg, "connection reset") {
					errMsg += ". This site likely has bot protection. Use browser(action='navigate', url='...') + page_text() instead."
				}
				return oasis.ToolResult{Error: errMsg}, nil
			}
			content := res.Content
			if res.Title != "" {
				content = "Title: " + res.Title + "\n\n" + content
			}
			return oasis.TextResult(content), nil
		})
}

func workspaceInfoTool(sb Sandbox) toolImpl {
	return newTool("workspace_info",
		"Get information about the sandbox environment: OS, architecture, working directory, installed tools (rg, fd, git, python3, node, etc), and browser availability. Call this once at the start of a session to understand your environment.",
		string(core.DeriveSchema[emptyArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			res, err := sb.WorkspaceInfo(ctx)
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			data, _ := json.Marshal(res)
			return oasis.TextResult(string(data)), nil
		})
}

func browserTool(sb BrowserSandbox) toolImpl {
	return newTool("browser",
		"Interact with the sandbox browser. Use element refs from the snapshot tool for precise interactions. IMPORTANT: click, type, fill, hover, focus, and select actions REQUIRE a ref (element reference) or coordinates — there is no implicit focus.",
		string(core.DeriveSchema[browserArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p browserArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			if p.Action == "navigate" && p.URL != "" {
				if err := sb.BrowserNavigate(ctx, p.URL); err != nil {
					return oasis.ToolResult{Error: err.Error()}, nil
				}
				return oasis.TextResult("navigated to " + p.URL), nil
			}
			// Validate that target-element actions have a ref or coordinates.
			switch p.Action {
			case "click", "type", "fill", "hover", "select", "focus":
				if p.Ref == "" && p.X == 0 && p.Y == 0 {
					return oasis.ToolResult{Error: fmt.Sprintf(
						"%s action requires a 'ref' (element reference from snapshot) or x/y coordinates. "+
							"Use the snapshot tool first to find the element ref, then pass it as ref (e.g., ref: 'e5').", p.Action,
					)}, nil
				}
			case "scroll":
				if p.Direction == "" {
					return oasis.ToolResult{Error: "scroll action requires 'direction' parameter (up, down, left, right)"}, nil
				}
			}
			if (p.Action == "type" || p.Action == "fill") && p.Text == "" {
				return oasis.ToolResult{Error: p.Action + " action requires 'text' parameter"}, nil
			}
			if p.Action == "select" && p.Value == "" {
				return oasis.ToolResult{Error: "select action requires 'value' parameter"}, nil
			}
			res, err := sb.BrowserAction(ctx, BrowserAction{
				Type:      p.Action,
				Ref:       p.Ref,
				X:         p.X,
				Y:         p.Y,
				Text:      p.Text,
				Key:       p.Key,
				Direction: p.Direction,
				Value:     p.Value,
			})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(res.Message), nil
		})
}

func screenshotTool(sb BrowserSandbox) toolImpl {
	return newTool("screenshot",
		"Take a screenshot of the sandbox browser",
		string(core.DeriveSchema[emptyArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			data, err := sb.BrowserScreenshot(ctx)
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(fmt.Sprintf("screenshot captured (%d bytes)", len(data))), nil
		})
}

func snapshotTool(sb BrowserSandbox) toolImpl {
	return newTool("snapshot",
		"Get the accessibility tree of the current browser page. Returns element references (e0, e1, ...) that can be used with the browser tool for precise interactions.",
		string(core.DeriveSchema[snapshotArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p snapshotArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			snap, err := sb.BrowserSnapshot(ctx, SnapshotOpts{
				Filter:   p.Filter,
				Selector: p.Selector,
				Depth:    p.Depth,
			})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			var out strings.Builder
			fmt.Fprintf(&out, "url: %s\ntitle: %s\n", snap.URL, snap.Title)
			for _, n := range snap.Nodes {
				fmt.Fprintf(&out, "[%s] %s %q\n", n.Ref, n.Role, n.Name)
			}
			return oasis.TextResult(out.String()), nil
		})
}

func pageTextTool(sb BrowserSandbox) toolImpl {
	return newTool("page_text",
		"Extract readable text content from the current browser page. Ideal for RAG and information gathering — much cheaper than screenshots.",
		string(core.DeriveSchema[pageTextArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p pageTextArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			result, err := sb.BrowserText(ctx, TextOpts{Raw: p.Raw, MaxChars: p.MaxChars})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(result.Text), nil
		})
}

func exportPDFTool(sb BrowserSandbox) toolImpl {
	return newTool("export_pdf",
		"Export the current browser page as a PDF document.",
		string(core.DeriveSchema[emptyArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			data, err := sb.BrowserPDF(ctx)
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(fmt.Sprintf("pdf exported (%d bytes)", len(data))), nil
		})
}

func webSearchTool(sb Sandbox) toolImpl {
	return newTool("web_search",
		"Search the web and return structured results (titles, URLs, snippets). Use this to find relevant pages before fetching or browsing them. Returns up to 10 results by default.",
		string(core.DeriveSchema[webSearchArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p webSearchArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			res, err := sb.WebSearch(ctx, WebSearchRequest{Query: p.Query, MaxResults: p.MaxResults})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if len(res.Results) == 0 {
				return oasis.TextResult("No results found for: " + p.Query), nil
			}
			var out strings.Builder
			fmt.Fprintf(&out, "Found %d results for: %s\n\n", len(res.Results), res.Query)
			for i, r := range res.Results {
				fmt.Fprintf(&out, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
				if r.Snippet != "" {
					fmt.Fprintf(&out, "   %s\n", r.Snippet)
				}
				out.WriteString("\n")
			}
			return oasis.TextResult(out.String()), nil
		})
}

func browserEvalTool(sb BrowserSandbox) toolImpl {
	return newTool("browser_eval",
		"Execute JavaScript in the current browser tab. Useful for reading form values, checking element states, extracting data, or interacting with page APIs that aren't accessible through the accessibility tree.",
		string(core.DeriveSchema[browserEvalArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p browserEvalArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			result, err := sb.BrowserEval(ctx, p.Expression)
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(result), nil
		})
}

func browserFindTool(sb BrowserSandbox) toolImpl {
	return newTool("browser_find",
		"Find an element ref using a natural-language description instead of manually searching the snapshot. Returns the best matching element ref, confidence level, and score.",
		string(core.DeriveSchema[browserFindArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p browserFindArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			result, err := sb.BrowserFind(ctx, p.Query)
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.TextResult(fmt.Sprintf("ref: %s (confidence: %s, score: %.2f)", result.Ref, result.Confidence, result.Score)), nil
		})
}

func browserWaitTool(sb BrowserSandbox) toolImpl {
	return newTool("browser_wait",
		"Wait for a page condition after navigate/click instead of polling with screenshots. Returns satisfied=false on timeout (never errors) — if not satisfied, take a snapshot to inspect the actual page state.",
		string(core.DeriveSchema[browserWaitArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p browserWaitArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			if p.Kind == "" {
				return oasis.ToolResult{Error: "kind is required (selector, text, url, load, time, function)"}, nil
			}
			res, err := sb.BrowserWait(ctx, BrowserWaitOpts{
				Kind:      p.Kind,
				Value:     p.Value,
				TimeoutMs: p.TimeoutMs,
				State:     p.State,
			})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if res.Satisfied {
				return oasis.TextResult(fmt.Sprintf("condition met (%s) after %dms", res.Kind, res.ElapsedMs)), nil
			}
			msg := fmt.Sprintf("condition NOT met (%s) after %dms", res.Kind, res.ElapsedMs)
			if res.Detail != "" {
				msg += ": " + res.Detail
			}
			return oasis.TextResult(msg + ". Take a snapshot to inspect the current page state."), nil
		})
}

func mcpCallTool(sb Sandbox) toolImpl {
	return newTool("mcp_call",
		"Invoke an MCP tool on a server in the sandbox",
		string(core.DeriveSchema[mcpCallArgs]()),
		func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
			var p mcpCallArgs
			if err := json.Unmarshal(args, &p); err != nil {
				return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
			}
			res, err := sb.MCPCall(ctx, MCPRequest{Server: p.Server, Tool: p.Tool, Args: p.Args})
			if err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			if res.IsError {
				return oasis.ToolResult{Error: res.Content}, nil
			}
			return oasis.TextResult(res.Content), nil
		})
}

// maxDeliverFileBytes caps the file size for deliver_file to prevent
// unbounded memory allocation when reading sandbox files into memory.
const maxDeliverFileBytes = 100 * 1024 * 1024 // 100 MB

// deliverFile implements oasis.StreamingAnyTool so it can emit a file_attachment
// event on the shared stream channel alongside the normal tool result.
type deliverFile struct {
	def     oasis.ToolDefinition
	sandbox Sandbox
	cfg     *toolsConfig
}

func deliverFileTool(sb Sandbox, cfg *toolsConfig) *deliverFile {
	return &deliverFile{
		def: oasis.ToolDefinition{
			Name: "deliver_file",
			Description: "Deliver a file from the sandbox to the user. The file will appear as a downloadable " +
				"attachment in the conversation. Use this after creating a file the user needs (reports, charts, " +
				"converted documents, generated code, etc).",
			Parameters: core.DeriveSchema[deliverFileArgs](),
		},
		sandbox: sb,
		cfg:     cfg,
	}
}

func (t *deliverFile) Name() string                     { return t.def.Name }
func (t *deliverFile) Definition() oasis.ToolDefinition { return t.def }

func (t *deliverFile) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	return t.executeDelivery(ctx, args, nil)
}

func (t *deliverFile) ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- oasis.StreamEvent) (oasis.ToolResult, error) {
	return t.executeDelivery(ctx, args, ch)
}

func (t *deliverFile) executeDelivery(ctx context.Context, args json.RawMessage, ch chan<- oasis.StreamEvent) (oasis.ToolResult, error) {
	var p deliverFileArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Path == "" {
		return oasis.ToolResult{Error: "path is required"}, nil
	}

	displayName := p.Name
	if displayName == "" {
		displayName = filepath.Base(p.Path)
	}

	// Detect MIME type from file extension; fall back to octet-stream.
	mimeType := mime.TypeByExtension(filepath.Ext(p.Path))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Download file from sandbox.
	rc, err := t.sandbox.DownloadFile(ctx, p.Path)
	if err != nil {
		return oasis.ToolResult{Error: "download failed: " + err.Error()}, nil
	}
	defer rc.Close()

	// Read into memory (bounded) to get size before delivering.
	data, err := io.ReadAll(io.LimitReader(rc, maxDeliverFileBytes+1))
	if err != nil {
		return oasis.ToolResult{Error: "read failed: " + err.Error()}, nil
	}
	if len(data) > maxDeliverFileBytes {
		return oasis.ToolResult{Error: fmt.Sprintf("file too large (max %s)", humanSize(maxDeliverFileBytes))}, nil
	}

	size := int64(len(data))

	// Routing: prefer an explicit mount that covers the path; fall back to
	// the legacy FileDelivery; otherwise error out with a clear message.
	url := ""
	if t.cfg != nil && len(t.cfg.mounts) > 0 {
		mount, key := findMountForPath(t.cfg.mounts, p.Path)
		if mount != nil && mount.Mode.Writable() {
			ver := ""
			if t.cfg.manifest != nil {
				ver, _ = t.cfg.manifest.Version(mount.Path, key)
			}
			newVer, putErr := mount.Backend.Put(ctx, key, mimeType, size, bytes.NewReader(data), ver)
			if putErr != nil {
				return oasis.ToolResult{Error: "delivery failed: " + putErr.Error()}, nil
			}
			if t.cfg.manifest != nil {
				t.cfg.manifest.Record(mount.Path, key, MountEntry{Key: key, Size: size, MimeType: mimeType, Version: newVer})
			}
			// The framework emits a stable identifier; the host app
			// translates this to a real URL when serving the file.
			url = mount.Path + "/" + key
		}
	}
	if url == "" && t.cfg != nil && t.cfg.delivery != nil {
		var delErr error
		url, delErr = t.cfg.delivery.Deliver(ctx, displayName, mimeType, size, bytes.NewReader(data))
		if delErr != nil {
			return oasis.ToolResult{Error: "delivery failed: " + delErr.Error()}, nil
		}
	}
	if url == "" {
		return oasis.ToolResult{Error: fmt.Sprintf("path %q is not under any writeable mount and no FileDelivery is configured", p.Path)}, nil
	}

	// Emit file_attachment event if streaming.
	if ch != nil {
		eventData, _ := json.Marshal(map[string]any{
			"name":      displayName,
			"mime_type": mimeType,
			"size":      size,
			"url":       url,
		})
		select {
		case ch <- oasis.StreamEvent{
			Type:    oasis.EventFileAttachment,
			Name:    "deliver_file",
			Content: string(eventData),
		}:
		case <-ctx.Done():
		}
	}

	return oasis.TextResult(fmt.Sprintf("delivered %s (%s)", displayName, humanSize(size))), nil
}

// humanSize formats a byte count as a human-readable string.
func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// compile-time checks
var (
	_ oasis.AnyTool          = (*deliverFile)(nil)
	_ oasis.StreamingAnyTool = (*deliverFile)(nil)
)
