# Code Execution

This guide shows how to enable LLM code execution and covers common patterns. The LLM writes Python code, the framework executes it in a sandboxed subprocess, and the code can call any agent tool via `call_tool()`.

## When to Use This

Use code execution when:

- **Data flow between tools** — the result of one tool call determines the input to the next
- **Conditional logic** — the LLM needs if/else branching based on tool results
- **Loops and iteration** — processing a list of items with tool calls per item
- **Data transformation** — parsing, filtering, aggregating results before returning

If the LLM just needs to call multiple independent tools at once, use `WithPlanExecution()` instead — it's simpler and has no subprocess overhead.

## Quick Start

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/code"
)

runner := code.NewSubprocessRunner("python3")

agent := oasis.NewLLMAgent("analyst", "Data analyst with code execution", provider,
    oasis.WithTools(searchTool, fileTool, httpTool),
    oasis.WithCodeExecution(runner),
    oasis.WithPrompt("You can execute Python code to accomplish complex tasks. "+
        "Use call_tool() to access your tools from within code. "+
        "Always return structured results via set_result()."),
)

result, err := agent.Execute(ctx, oasis.AgentTask{
    Input: "Find the top 3 trending Go repositories and summarize each one",
})
```

The agent now has access to an `execute_code` tool alongside its regular tools. The LLM decides when code execution is more appropriate than direct tool calls.

## Pattern 1: Sequential Tool Chains

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

## Pattern 2: Conditional Logic

When the next action depends on a previous result:

```python
# LLM generates this code
status = call_tool('http_fetch', {'url': 'https://api.example.com/health'})

if status['code'] == 200:
    data = call_tool('http_fetch', {'url': 'https://api.example.com/metrics'})
    set_result({'healthy': True, 'metrics': data})
else:
    # Try backup endpoint
    backup = call_tool('http_fetch', {'url': 'https://backup.example.com/health'})
    set_result({'healthy': False, 'primary_status': status['code'], 'backup': backup})
```

## Pattern 3: Error-Resilient Processing

Using try/except to handle partial failures:

```python
# LLM generates this code
urls = ['https://api1.example.com', 'https://api2.example.com', 'https://api3.example.com']

results = []
errors = []
for url in urls:
    try:
        data = call_tool('http_fetch', {'url': url})
        results.append({'url': url, 'data': data})
    except RuntimeError as e:
        errors.append({'url': url, 'error': str(e)})
        print(f"Failed to fetch {url}: {e}")  # goes to logs

set_result({
    'successful': len(results),
    'failed': len(errors),
    'results': results,
    'errors': errors,
})
```

## Pattern 4: Parallel Fan-Out with Processing

Combine parallel tool calls with post-processing:

```python
# LLM generates this code
# Fetch multiple pages in parallel
pages = call_tools_parallel([
    ('http_fetch', {'url': 'https://api.example.com/users'}),
    ('http_fetch', {'url': 'https://api.example.com/orders'}),
    ('http_fetch', {'url': 'https://api.example.com/products'}),
])

# Process results with Python logic
users, orders, products = pages
active_users = [u for u in users if u.get('active')]
recent_orders = [o for o in orders if o.get('days_ago', 999) < 7]

set_result({
    'active_users': len(active_users),
    'recent_orders': len(recent_orders),
    'total_products': len(products),
    'top_users': sorted(active_users, key=lambda u: u.get('score', 0), reverse=True)[:5],
})
```

## Combining with Plan Execution

You can enable both `WithPlanExecution()` and `WithCodeExecution()` on the same agent. The LLM chooses the right tool for the job:

```go
agent := oasis.NewLLMAgent("analyst", "Data analyst", provider,
    oasis.WithTools(searchTool, fileTool),
    oasis.WithPlanExecution(),     // simple parallel fan-out
    oasis.WithCodeExecution(runner), // complex logic
    oasis.WithPrompt(`You have two execution modes:
- execute_plan: Use for simple parallel tool calls with no dependencies
- execute_code: Use for complex logic with conditionals, loops, or data flow between steps
Choose the simplest mode that handles the task.`),
)
```

## Configuration

### Timeout

Set a longer timeout for heavy processing:

```go
runner := code.NewSubprocessRunner("python3",
    code.WithTimeout(2 * time.Minute),
)
```

### Workspace

Restrict file access to a specific directory:

```go
runner := code.NewSubprocessRunner("python3",
    code.WithWorkspace("/var/sandbox/agent-workspace"),
)
```

The Python code can read and write files within this directory:

```python
# OK — within workspace
with open("results.json", "w") as f:
    json.dump(data, f)

# PermissionError — outside workspace
open("/etc/passwd")
```

### Max Output

Limit the size of captured stdout/stderr:

```go
runner := code.NewSubprocessRunner("python3",
    code.WithMaxOutput(1 << 20), // 1 MB max output
)
```

### Environment Variables

Pass specific environment variables:

```go
runner := code.NewSubprocessRunner("python3",
    code.WithEnv("API_KEY", os.Getenv("EXTERNAL_API_KEY")),
    code.WithEnv("ENVIRONMENT", "production"),
)
```

Or pass all host environment variables:

```go
runner := code.NewSubprocessRunner("python3",
    code.WithEnvPassthrough(),
)
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
- **Use `set_result()`.** If the code doesn't call `set_result()`, the agent gets a message saying no result was set. Always return structured data.
- **Use `print()` for debugging.** Print output goes to `CodeResult.Logs`, not the structured output. Useful for debugging but invisible to the LLM unless there's an error.
- **Handle errors in code.** Wrap tool calls in try/except when partial failures are acceptable. Unhandled exceptions terminate execution with the traceback in logs.
- **Keep code simple.** The LLM writes better code when the task is clear. Complex data processing is fine; complex infrastructure code is not.
- **Python 3 required.** The subprocess runner requires `python3` in PATH. Standard library modules are available; third-party packages depend on the host's Python environment.

## See Also

- [Code Execution Concept](../concepts/code-execution.md) — architecture, safety model, Python API reference
- [Tool Concept](../concepts/tool.md) — plan execution, parallel execution
- [Execution Plans](execution-plans.md) — Workflow-based plan-approve-execute pattern
- [Custom Tool Guide](custom-tool.md) — build tools that code can call
