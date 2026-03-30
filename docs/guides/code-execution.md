# Code Execution

This guide shows how to enable sandbox capabilities for your agents. The sandbox provides a full Docker container with shell access, code execution, file I/O, browser automation, and MCP integration — all auto-registered as agent tools.

## When to Use This

Use sandbox when:

- **Code execution** — the LLM needs to write and run Python, Node.js, or Bash code
- **Shell commands** — running system commands, installing packages, managing processes
- **File operations** — reading, writing, uploading, or downloading files in an isolated environment
- **Browser automation** — navigating web pages, taking screenshots, interacting with web UIs
- **MCP integration** — calling MCP server tools from within the sandbox
- **Data flow between tools** — the result of one tool call determines the input to the next
- **Conditional logic** — if/else branching based on tool results
- **Visualization** — generating charts, images, or files using Python/Node.js libraries

If the LLM just needs to call multiple independent tools at once, use `WithPlanExecution()` instead — it's simpler and has no container overhead.

## Quick Start

### 1. Prerequisites

- Docker Engine 20.10+

### 2. Create a sandbox manager and agent

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/sandbox"
    "github.com/nevindra/oasis/sandbox/ix"
)

// Create sandbox manager (manages Docker containers)
mgr, err := ix.NewManager(ctx, ix.ManagerConfig{
    Image: "oasis-ix:latest",
})

// Create a sandbox for a session
sb, err := mgr.Create(ctx, sandbox.CreateOpts{
    SessionID: "conversation-123",
    TTL:       time.Hour,
})
defer sb.Close()

agent := oasis.NewLLMAgent("analyst", "Data analyst with sandbox", provider,
    oasis.WithTools(searchTool, fileTool, httpTool),
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),
    oasis.WithPrompt("You have access to a sandbox environment with shell, code execution, "+
        "file I/O, and browser capabilities. Use the appropriate tool for each task."),
)

result, err := agent.Execute(ctx, oasis.AgentTask{
    Input: "Find the top 3 trending Go repositories and summarize each one",
})
```

The agent now has access to 10 sandbox tools alongside its regular tools. The LLM decides which tool to use based on the task.

### Docker Compose

For production setups:

```yaml
services:
  app:
    build: .
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

The `ix.Manager` manages Docker containers directly — no separate sandbox service needed. Your app just needs access to the Docker socket.

## Python Patterns

### Pattern 1: Sequential Tool Chains

When one tool's output feeds into the next:

```python
# LLM generates this code
urls = call_tool('web_search', {'query': 'top Go repositories 2026'})

summaries = []
for url in urls['results'][:3]:
    page = call_tool('http_fetch', {'url': url['link']})
    summaries.append({
        'title': url['title'],
        'url': url['link'],
        'content_length': len(page['body']),
    })

set_result({'repositories': summaries, 'count': len(summaries)})
```

With regular tool calling, this would take 4+ LLM round-trips. With code execution, it's 1 LLM call + 4 tool dispatches.

### Pattern 2: Conditional Logic

When the next action depends on a previous result:

```python
status = call_tool('http_fetch', {'url': 'https://api.example.com/health'})

if status['code'] == 200:
    data = call_tool('http_fetch', {'url': 'https://api.example.com/metrics'})
    set_result({'healthy': True, 'metrics': data})
else:
    backup = call_tool('http_fetch', {'url': 'https://backup.example.com/health'})
    set_result({'healthy': False, 'primary_status': status['code'], 'backup': backup})
```

### Pattern 3: Parallel Fan-Out with Processing

Combine parallel tool calls with post-processing:

```python
pages = call_tools_parallel([
    ('http_fetch', {'url': 'https://api.example.com/users'}),
    ('http_fetch', {'url': 'https://api.example.com/orders'}),
    ('http_fetch', {'url': 'https://api.example.com/products'}),
])

users, orders, products = pages
active_users = [u for u in users if u.get('active')]
recent_orders = [o for o in orders if o.get('days_ago', 999) < 7]

set_result({
    'active_users': len(active_users),
    'recent_orders': len(recent_orders),
    'total_products': len(products),
})
```

### Pattern 4: Data Visualization

Generate charts and return them as files:

```python
import matplotlib.pyplot as plt

data = call_tool('query_db', {'sql': 'SELECT month, revenue FROM sales'})

months = [r['month'] for r in data]
revenue = [r['revenue'] for r in data]

plt.figure(figsize=(10, 6))
plt.bar(months, revenue)
plt.title('Monthly Revenue')
plt.savefig('chart.png', dpi=150)

set_result({'months': len(months), 'total': sum(revenue)}, files=['chart.png'])
```

The chart is returned as an attachment in the agent result.

### Pattern 5: On-the-fly Package Install

Install packages at runtime when you need something not pre-installed:

```python
install_package('httpx')
import httpx

resp = httpx.get('https://api.example.com/data')
set_result(resp.json())
```

## Node.js Patterns

All the same patterns work in Node.js. Tool functions are async — use `await`.

### Sequential Tool Chain

```javascript
const urls = await callTool('web_search', { query: 'top Go repositories 2026' });

const summaries = [];
for (const url of urls.results.slice(0, 3)) {
    const page = await callTool('http_fetch', { url: url.link });
    summaries.push({
        title: url.title,
        url: url.link,
        contentLength: page.body.length,
    });
}

setResult({ repositories: summaries, count: summaries.length });
```

### Parallel Fan-Out

```javascript
const [users, orders, products] = await callToolsParallel([
    ['http_fetch', { url: 'https://api.example.com/users' }],
    ['http_fetch', { url: 'https://api.example.com/orders' }],
    ['http_fetch', { url: 'https://api.example.com/products' }],
]);

const activeUsers = users.filter(u => u.active);
setResult({
    active_users: activeUsers.length,
    total_orders: orders.length,
    total_products: products.length,
});
```

### On-the-fly Package Install

```javascript
installPackage('cheerio');
const cheerio = require('cheerio');

const page = await callTool('http_fetch', { url: 'https://example.com' });
const $ = cheerio.load(page.body);
const title = $('title').text();

setResult({ title });
```

## File I/O

### Input Files

Pass files to code execution via `CodeRequest.Files`:

```go
result, err := runner.Run(ctx, oasis.CodeRequest{
    Code:    "import pandas as pd\ndf = pd.read_csv('data.csv')\nset_result({'rows': len(df)})",
    Runtime: "python",
    Files: []oasis.CodeFile{
        {Name: "data.csv", Data: csvBytes},
    },
}, dispatch)
```

Files are written to the workspace before code executes.

### Output Files

Code declares output files via `set_result(files=[...])`:

```python
import matplotlib.pyplot as plt
plt.plot([1, 2, 3], [1, 4, 9])
plt.savefig('chart.png')
set_result("chart generated", files=['chart.png'])
```

Output files are returned in `CodeResult.Files` with MIME types auto-detected. The agent maps them to `Attachment` structs.

### Surgical File Edits

Use `file_edit` for targeted string replacements instead of reading and rewriting entire files:

```python
# LLM calls file_edit tool directly — no code execution needed
# tool: file_edit
# args: {"path": "/app/main.py", "old_string": "DEBUG = True", "new_string": "DEBUG = False"}
```

The old string must appear exactly once in the file. This saves significant tokens compared to reading the file, modifying it in code, and writing it back.

### Finding Files with Glob

Use `file_glob` to find files by pattern:

```python
# tool: file_glob
# args: {"pattern": "**/*.py", "path": "/app"}
# returns: /app/main.py\n/app/lib/utils.py\n/app/tests/test_main.py
```

Supports `**` for recursive matching. Backed by `fd` for fast results.

### Searching Code with Grep

Use `file_grep` to search file contents by regex:

```python
# tool: file_grep
# args: {"pattern": "def main", "path": "/app", "glob": "*.py"}
# returns: /app/main.py:42: def main():
```

Returns file paths, line numbers, and matching line content. Backed by `rg` (ripgrep) for fast search across large codebases.

## Session Persistence

Use `SessionID` to persist workspace files across executions:

```go
req := oasis.CodeRequest{
    Code:      "open('state.json', 'w').write('{\"count\": 1}')\nset_result('saved')",
    Runtime:   "python",
    SessionID: "user-123",
}
```

A subsequent execution with `SessionID: "user-123"` will find `state.json` in the workspace. Sessions are cleaned up automatically after the TTL expires, or explicitly via `DELETE /workspace/{session_id}`.

## Combining with Plan Execution

You can enable both `WithPlanExecution()` and `WithSandbox()` on the same agent:

```go
agent := oasis.NewLLMAgent("analyst", "Data analyst", provider,
    oasis.WithTools(searchTool, fileTool),
    oasis.WithPlanExecution(),                    // simple parallel fan-out
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),  // sandbox tools
    oasis.WithPrompt(`You have multiple execution modes:
- execute_plan: Use for simple parallel tool calls with no dependencies
- execute_code: Use for complex logic with conditionals, loops, or data flow between steps
- shell: Use for system commands
- file_read/file_write: Use for file operations
Choose the simplest mode that handles the task.`),
)
```

## Configuration

### Manager Options

```go
mgr, err := ix.NewManager(ctx, ix.ManagerConfig{
    Image: "oasis-ix:latest",
})
```

### Sandbox Options

```go
sb, err := mgr.Create(ctx, sandbox.CreateOpts{
    SessionID: "user-123",
    TTL:       time.Hour,
})
```

### Network

The Network agent supports sandbox the same way:

```go
network := oasis.NewNetwork("coordinator", "Multi-agent coordinator", router,
    oasis.WithAgents(researcher, writer),
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),
)
```

## Tips

- **Prompt the LLM clearly.** Tell it when to use `execute_code` vs `shell` vs regular tool calls. Without guidance, some LLMs default to one-at-a-time tool calls.
- **Use `set_result()` / `setResult()`.** If code doesn't set a result, the agent gets a message saying no result was set. Always return structured data.
- **Use `print()` / `console.log()` for debugging.** Output goes to `CodeResult.Logs`, not the structured result.
- **Handle errors in code.** Wrap tool calls in try/except or try/catch when partial failures are acceptable.
- **Keep code simple.** The LLM writes better code when the task is clear.
- **Declare output files explicitly.** Files are only returned if listed in `set_result(files=[...])` / `setResult(data, ['file.png'])`.
- **Choose the right runtime.** Python for data analysis, visualization, scientific computing. Node.js for web scraping, JSON processing, or when the LLM prefers JavaScript.
- **Use `shell` for simple commands.** One-off system commands are better as `shell` calls than `execute_code`.
- **Use `file_read`/`file_write` for direct file access.** Simpler than writing code to read/write files.
- **Use `file_edit` for surgical changes.** Replacing a single string is far cheaper than reading the whole file and rewriting it.
- **Use `file_glob`/`file_grep` for discovery.** Faster and more structured than running `find` or `grep` via shell.

## See Also

- [Sandbox Concept](../concepts/sandbox.md) — architecture, safety model, sandbox interface reference
- [Tool Concept](../concepts/tool.md) — plan execution, parallel execution
- [Execution Plans](execution-plans.md) — Workflow-based plan-approve-execute pattern
- [Custom Tool Guide](custom-tool.md) — build tools that code can call
