# Code Execution

This guide shows how to enable LLM code execution and covers common patterns. The LLM writes Python or Node.js code, the framework sends it to a sandbox service for execution, and the code can call any agent tool via `call_tool()` / `callTool()`.

## When to Use This

Use code execution when:

- **Data flow between tools** — the result of one tool call determines the input to the next
- **Conditional logic** — the LLM needs if/else branching based on tool results
- **Loops and iteration** — processing a list of items with tool calls per item
- **Data transformation** — parsing, filtering, aggregating results before returning
- **Visualization** — generating charts, images, or files using Python/Node.js libraries

If the LLM just needs to call multiple independent tools at once, use `WithPlanExecution()` instead — it's simpler and has no subprocess overhead.

## Quick Start

### 1. Start the sandbox container

```bash
docker build -f cmd/sandbox/Dockerfile -t oasis-sandbox .
docker run -d --name sandbox -p 9000:9000 oasis-sandbox
```

### 2. Create an HTTPRunner and agent

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/code"
)

runner := code.NewHTTPRunner("http://localhost:9000")
defer runner.Close()

agent := oasis.NewLLMAgent("analyst", "Data analyst with code execution", provider,
    oasis.WithTools(searchTool, fileTool, httpTool),
    oasis.WithCodeExecution(runner),
    oasis.WithPrompt("You can execute Python or Node.js code to accomplish complex tasks. "+
        "Use call_tool()/callTool() to access your tools from within code. "+
        "Always return structured results via set_result()/setResult()."),
)

result, err := agent.Execute(ctx, oasis.AgentTask{
    Input: "Find the top 3 trending Go repositories and summarize each one",
})
```

The agent now has access to an `execute_code` tool alongside its regular tools. The LLM decides when code execution is more appropriate than direct tool calls.

### Docker Compose

For production setups, run the sandbox as a sidecar:

```yaml
services:
  app:
    build: .
    depends_on: [sandbox]

  sandbox:
    build:
      context: .
      dockerfile: cmd/sandbox/Dockerfile
    environment:
      SANDBOX_MAX_CONCURRENT: "8"
      SANDBOX_SESSION_TTL: "2h"
```

Then point HTTPRunner at the service name:

```go
runner := code.NewHTTPRunner("http://sandbox:9000")
```

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

You can enable both `WithPlanExecution()` and `WithCodeExecution()` on the same agent:

```go
agent := oasis.NewLLMAgent("analyst", "Data analyst", provider,
    oasis.WithTools(searchTool, fileTool),
    oasis.WithPlanExecution(),      // simple parallel fan-out
    oasis.WithCodeExecution(runner), // complex logic
    oasis.WithPrompt(`You have two execution modes:
- execute_plan: Use for simple parallel tool calls with no dependencies
- execute_code: Use for complex logic with conditionals, loops, or data flow between steps
Choose the simplest mode that handles the task.`),
)
```

## Configuration

### HTTPRunner Options

```go
runner := code.NewHTTPRunner("http://sandbox:9000",
    code.WithTimeout(2 * time.Minute),     // execution timeout
    code.WithMaxFileSize(20 << 20),        // 20MB max per file
    code.WithMaxRetries(3),                // retry on transient errors
    code.WithCallbackAddr("0.0.0.0:0"),    // callback listen address
)
```

### External Callback Mount

If your app already runs an HTTP server, avoid the extra listener:

```go
runner := code.NewHTTPRunner("http://sandbox:9000",
    code.WithCallbackExternal("http://myapp:8080"),
)
// Mount on your server's mux:
mux.Handle("/_oasis/dispatch", runner.Handler())
```

### Network

The Network agent supports code execution the same way:

```go
network := oasis.NewNetwork("coordinator", "Multi-agent coordinator", router,
    oasis.WithAgents(researcher, writer),
    oasis.WithCodeExecution(runner),
)
```

The code can call both regular tools and `agent_*` tools for delegating to subagents.

## Tips

- **Prompt the LLM clearly.** Tell it when to use `execute_code` vs regular tool calls. Without guidance, some LLMs default to one-at-a-time tool calls.
- **Use `set_result()` / `setResult()`.** If code doesn't set a result, the agent gets a message saying no result was set. Always return structured data.
- **Use `print()` / `console.log()` for debugging.** Output goes to `CodeResult.Logs`, not the structured result.
- **Handle errors in code.** Wrap tool calls in try/except or try/catch when partial failures are acceptable.
- **Keep code simple.** The LLM writes better code when the task is clear.
- **Declare output files explicitly.** Files are only returned if listed in `set_result(files=[...])` / `setResult(data, ['file.png'])`.
- **Choose the right runtime.** Python for data analysis, visualization, scientific computing. Node.js for web scraping, JSON processing, or when the LLM prefers JavaScript.

## See Also

- [Code Execution Concept](../concepts/code-execution.md) — architecture, safety model, runtime API reference
- [Tool Concept](../concepts/tool.md) — plan execution, parallel execution
- [Execution Plans](execution-plans.md) — Workflow-based plan-approve-execute pattern
- [Custom Tool Guide](custom-tool.md) — build tools that code can call
