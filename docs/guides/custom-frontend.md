# Building a Custom Frontend

Implement the `Frontend` interface to add support for a new messaging platform — Discord, Slack, HTTP API, CLI, or anything else.

## Implement Frontend

```go
package discord

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type Bot struct {
    token string
}

func New(token string) *Bot {
    return &Bot{token: token}
}

func (b *Bot) Poll(ctx context.Context) (<-chan oasis.IncomingMessage, error) {
    ch := make(chan oasis.IncomingMessage)
    go func() {
        defer close(ch)
        // Listen for messages from your platform
        // Convert each to oasis.IncomingMessage
        // Respect ctx.Done() for graceful shutdown
        for {
            select {
            case <-ctx.Done():
                return
            default:
                msg := waitForMessage()
                ch <- oasis.IncomingMessage{
                    ID:     msg.ID,
                    ChatID: msg.ChannelID,
                    UserID: msg.AuthorID,
                    Text:   msg.Content,
                }
            }
        }
    }()
    return ch, nil
}

func (b *Bot) Send(ctx context.Context, chatID, text string) (string, error) {
    // Send message to your platform
    // Return the message ID for later editing
    return msgID, nil
}

func (b *Bot) Edit(ctx context.Context, chatID, msgID, text string) error {
    // Edit with plain text
    return nil
}

func (b *Bot) EditFormatted(ctx context.Context, chatID, msgID, text string) error {
    // Edit with rich formatting (receives HTML — convert to your format)
    return nil
}

func (b *Bot) SendTyping(ctx context.Context, chatID string) error {
    // Show typing indicator
    return nil
}

func (b *Bot) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
    // Download file, return bytes + filename
    return data, filename, nil
}

// compile-time check
var _ oasis.Frontend = (*Bot)(nil)
```

## Key Requirements

- `Poll` runs in a goroutine, pushes messages to the channel, and respects `ctx.Done()`
- `Send` returns a message ID usable with `Edit`/`EditFormatted`
- `EditFormatted` receives HTML — convert to your platform's format as needed

## CLI Frontend Example

A minimal frontend for terminal interaction:

```go
package cli

import (
    "bufio"
    "context"
    "fmt"
    "os"

    oasis "github.com/nevindra/oasis"
)

type CLI struct{}

func New() *CLI { return &CLI{} }

func (c *CLI) Poll(ctx context.Context) (<-chan oasis.IncomingMessage, error) {
    ch := make(chan oasis.IncomingMessage)
    go func() {
        defer close(ch)
        scanner := bufio.NewScanner(os.Stdin)
        for {
            fmt.Print("> ")
            if !scanner.Scan() { return }
            ch <- oasis.IncomingMessage{
                ChatID: "cli",
                UserID: "user",
                Text:   scanner.Text(),
            }
        }
    }()
    return ch, nil
}

func (c *CLI) Send(_ context.Context, _, text string) (string, error) {
    fmt.Println(text)
    return "msg-1", nil
}

func (c *CLI) Edit(_ context.Context, _, _, text string) error {
    fmt.Print("\r" + text)
    return nil
}

func (c *CLI) EditFormatted(_ context.Context, _, _, text string) error {
    fmt.Println(text)
    return nil
}

func (c *CLI) SendTyping(_ context.Context, _ string) error { return nil }
func (c *CLI) DownloadFile(_ context.Context, _ string) ([]byte, string, error) {
    return nil, "", fmt.Errorf("not supported")
}
```

## See Also

- [Frontend Concept](../concepts/frontend.md)
- [Streaming Guide](streaming.md) — the poll-send-edit pattern
