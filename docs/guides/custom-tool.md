# Building a Custom Tool

Tools are the primary extension point in Oasis. This guide walks through building a complete tool from scratch.

## Step 1: Implement the Tool Interface

```go
package weather

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"

    oasis "github.com/nevindra/oasis"
)

type Tool struct {
    apiKey string
}

func New(apiKey string) *Tool {
    return &Tool{apiKey: apiKey}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
    return []oasis.ToolDefinition{{
        Name:        "get_weather",
        Description: "Get current weather for a city.",
        Parameters: json.RawMessage(`{
            "type": "object",
            "properties": {
                "city": {
                    "type": "string",
                    "description": "City name (e.g. 'Jakarta', 'New York')"
                }
            },
            "required": ["city"]
        }`),
    }}
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    var params struct {
        City string `json:"city"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
    }

    weather, err := t.fetch(ctx, params.City)
    if err != nil {
        return oasis.ToolResult{Error: "weather API error: " + err.Error()}, nil
    }

    return oasis.ToolResult{Content: weather}, nil
}

func (t *Tool) fetch(ctx context.Context, city string) (string, error) {
    // Call your weather API here...
    return fmt.Sprintf("Weather in %s: 28°C, sunny", city), nil
}
```

## Step 2: Register the Tool

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(weather.New("weather-api-key")),
)
```

That's it. The tool's definitions are automatically included when the agent passes tool schemas to the LLM.

## Multi-Function Tools

A single Tool can expose multiple functions. Dispatch on the `name` parameter:

```go
func (t *Tool) Definitions() []oasis.ToolDefinition {
    return []oasis.ToolDefinition{
        {Name: "widget_create", Description: "Create a widget", Parameters: createSchema},
        {Name: "widget_list", Description: "List all widgets", Parameters: listSchema},
        {Name: "widget_delete", Description: "Delete a widget", Parameters: deleteSchema},
    }
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    switch name {
    case "widget_create":
        return t.create(ctx, args)
    case "widget_list":
        return t.list(ctx)
    case "widget_delete":
        return t.delete(ctx, args)
    default:
        return oasis.ToolResult{Error: "unknown tool: " + name}, nil
    }
}
```

## Error Handling

**Business errors go in `ToolResult.Error`**, not as Go errors:

```go
// Correct — the LLM sees the error and can adjust
return oasis.ToolResult{Error: "city not found: " + city}, nil

// Wrong — this is for infrastructure failures only
return oasis.ToolResult{}, fmt.Errorf("city not found: %s", city)
```

The Go `error` return should only be used for truly unexpected failures (e.g., nil pointer, panic recovery). The agent loop treats Go errors as infrastructure failures and may abort.

## Injecting Dependencies

Tools that need storage, embedding, or other services receive them via the constructor:

```go
func New(store oasis.Store, emb oasis.EmbeddingProvider) *MyTool {
    return &MyTool{store: store, embedding: emb}
}
```

No global state. No service locators. Dependencies are explicit in the function signature.

## JSON Schema Tips

Write clear descriptions — the LLM uses these to decide when and how to call your tool:

```json
{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "description": "Natural language search query. Be specific."
        },
        "limit": {
            "type": "integer",
            "description": "Maximum number of results to return. Default: 10, max: 50."
        },
        "format": {
            "type": "string",
            "enum": ["brief", "detailed"],
            "description": "Output format. 'brief' for summaries, 'detailed' for full content."
        }
    },
    "required": ["query"]
}
```

## See Also

- [Tool Concept](../concepts/tool.md)
- [API Reference: Interfaces](../api/interfaces.md)
