package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxMessageLength = 4096
	apiBaseURL       = "https://api.telegram.org/bot"
)

// Bot implements Frontend for Telegram.
type Bot struct {
	token      string
	httpClient *http.Client
}

// Compile-time check that Bot implements Frontend.
var _ Frontend = (*Bot)(nil)

// NewBot creates a new Telegram bot with the given token.
func NewBot(token string) *Bot {
	return &Bot{
		token:      token,
		httpClient: &http.Client{},
	}
}

// Poll starts long-polling for updates and returns a channel of incoming messages.
func (b *Bot) Poll(ctx context.Context) (<-chan IncomingMessage, error) {
	ch := make(chan IncomingMessage)
	go b.pollLoop(ctx, ch)
	return ch, nil
}

func (b *Bot) pollLoop(ctx context.Context, ch chan<- IncomingMessage) {
	defer close(ch)
	var offset int64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram: poll error: %v", err)
			continue
		}

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil {
				continue
			}
			msg := mapToIncoming(u.Message)
			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]Update, error) {
	body := map[string]any{
		"offset":          offset,
		"timeout":         30,
		"allowed_updates": []string{"message"},
	}
	var result []Update
	if err := b.callAPIWithCtx(ctx, "getUpdates", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Send sends a message with HTML formatting.
// If the text exceeds Telegram's 4096-char limit, it splits into multiple messages.
// Returns the message ID of the last sent message.
func (b *Bot) Send(ctx context.Context, chatID string, text string) (string, error) {
	chunks := splitMessage(text)

	var lastMsgID string
	for _, chunk := range chunks {
		html := MarkdownToHTML(chunk)
		body := map[string]any{
			"chat_id":    chatID,
			"text":       html,
			"parse_mode": "HTML",
		}
		var result TGMessage
		if err := b.callAPIWithCtx(ctx, "sendMessage", body, &result); err != nil {
			return "", err
		}
		lastMsgID = strconv.FormatInt(result.MessageID, 10)
	}

	return lastMsgID, nil
}

// Edit updates a message with plain text (no parse_mode).
// Silently ignores "message is not modified" errors.
func (b *Bot) Edit(ctx context.Context, chatID string, msgID string, text string) error {
	msgIDInt, err := strconv.ParseInt(msgID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid message ID %q: %w", msgID, err)
	}
	body := map[string]any{
		"chat_id":    chatID,
		"message_id": msgIDInt,
		"text":       text,
	}
	err = b.callAPIWithCtx(ctx, "editMessageText", body, nil)
	if err != nil && isNotModifiedError(err) {
		return nil
	}
	return err
}

// EditFormatted updates a message with HTML formatting.
// Converts markdown to HTML. Falls back to plain text Edit on failure.
func (b *Bot) EditFormatted(ctx context.Context, chatID string, msgID string, text string) error {
	msgIDInt, err := strconv.ParseInt(msgID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid message ID %q: %w", msgID, err)
	}

	html := MarkdownToHTML(text)
	body := map[string]any{
		"chat_id":    chatID,
		"message_id": msgIDInt,
		"text":       html,
		"parse_mode": "HTML",
	}
	err = b.callAPIWithCtx(ctx, "editMessageText", body, nil)
	if err == nil {
		return nil
	}
	if isNotModifiedError(err) {
		return nil
	}

	// HTML rejected -- fall back to plain text
	return b.Edit(ctx, chatID, msgID, text)
}

// SendTyping shows a typing indicator.
func (b *Bot) SendTyping(ctx context.Context, chatID string) error {
	body := map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	}
	return b.callAPIWithCtx(ctx, "sendChatAction", body, nil)
}

// DownloadFile downloads a file from Telegram.
// Two-step: getFile to obtain the file_path, then HTTP GET the file data.
func (b *Bot) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	// Step 1: get file path
	reqBody := map[string]any{
		"file_id": fileID,
	}
	var file TGFile
	if err := b.callAPIWithCtx(ctx, "getFile", reqBody, &file); err != nil {
		return nil, "", err
	}
	if file.FilePath == "" {
		return nil, "", fmt.Errorf("telegram: empty file_path for file_id %s", fileID)
	}

	// Step 2: download the file
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.token, file.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("telegram: create download request: %w", err)
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("telegram: download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("telegram: download file HTTP %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("telegram: read file body: %w", err)
	}

	// Extract filename from file_path (last segment)
	parts := strings.Split(file.FilePath, "/")
	filename := parts[len(parts)-1]

	return data, filename, nil
}

// callAPIWithCtx posts JSON to a Telegram Bot API method and decodes the result.
func (b *Bot) callAPIWithCtx(ctx context.Context, method string, reqBody any, result any) error {
	url := apiBaseURL + b.token + "/" + method

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("telegram: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("telegram: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: read response: %w", err)
	}

	// Parse the envelope to check ok/description
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description,omitempty"`
		ErrorCode   int             `json:"error_code,omitempty"`
		Result      json.RawMessage `json:"result,omitempty"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("telegram: decode response: %w (body: %s)", err, string(respBody))
	}

	if !envelope.OK {
		return &apiError{
			Code:        envelope.ErrorCode,
			Description: envelope.Description,
		}
	}

	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("telegram: decode result: %w", err)
		}
	}

	return nil
}

// apiError represents a Telegram API error response.
type apiError struct {
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram API error %d: %s", e.Code, e.Description)
}

// isNotModifiedError checks if the error is a Telegram "message is not modified" error.
func isNotModifiedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "message is not modified")
}

// mapToIncoming converts a Telegram Message to an IncomingMessage.
func mapToIncoming(m *TGMessage) IncomingMessage {
	msg := IncomingMessage{
		ID:     strconv.FormatInt(m.MessageID, 10),
		ChatID: strconv.FormatInt(m.Chat.ID, 10),
		Text:   m.Text,
	}

	if m.From != nil {
		msg.UserID = strconv.FormatInt(m.From.ID, 10)
	}

	if m.Caption != "" {
		msg.Caption = m.Caption
		if msg.Text == "" {
			msg.Text = m.Caption
		}
	}

	if m.Document != nil {
		msg.Document = &FileInfo{
			FileID:   m.Document.FileID,
			FileName: m.Document.FileName,
			MimeType: m.Document.MimeType,
			FileSize: m.Document.FileSize,
		}
	}

	if len(m.Photo) > 0 {
		msg.Photos = make([]FileInfo, len(m.Photo))
		for i, p := range m.Photo {
			msg.Photos[i] = FileInfo{
				FileID:   p.FileID,
				FileSize: p.FileSize,
			}
		}
	}

	if m.ReplyToMessage != nil {
		msg.ReplyToMsgID = strconv.FormatInt(m.ReplyToMessage.MessageID, 10)
	}

	return msg
}

// splitMessage splits text into chunks that fit within Telegram's 4096-char limit.
func splitMessage(text string) []string {
	if len(text) <= maxMessageLength {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxMessageLength {
			chunks = append(chunks, remaining)
			break
		}

		splitAt := remaining[:maxMessageLength]
		splitPos := strings.LastIndex(splitAt, "\n")
		if splitPos == -1 {
			splitPos = maxMessageLength
		} else {
			splitPos++ // include the newline in the current chunk
		}

		chunks = append(chunks, remaining[:splitPos])
		remaining = remaining[splitPos:]
	}

	return chunks
}
