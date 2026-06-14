package mcp

import "encoding/json"

// Unexported JSON-RPC wire structs for client-consumed MCP primitives. Public
// result types live in primitives.go; these mirror the on-the-wire shapes.

type wireResourcesList struct {
	Resources []wireResource `json:"resources"`
}

type wireResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type wireResourceRead struct {
	Contents []wireResourceContent `json:"contents"`
}

type wireResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type wirePromptsList struct {
	Prompts []wirePrompt `json:"prompts"`
}

type wirePrompt struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Arguments   []wirePromptArgument `json:"arguments,omitempty"`
}

type wirePromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type wirePromptGet struct {
	Description string          `json:"description,omitempty"`
	Messages    []wirePromptMsg `json:"messages"`
}

type wirePromptMsg struct {
	Role    string       `json:"role"`
	Content ContentBlock `json:"content"`
}

func decodePromptsList(raw []byte) ([]Prompt, error) {
	var w wirePromptsList
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]Prompt, len(w.Prompts))
	for i, p := range w.Prompts {
		args := make([]PromptArgument, len(p.Arguments))
		for j, a := range p.Arguments {
			args[j] = PromptArgument{Name: a.Name, Description: a.Description, Required: a.Required}
		}
		out[i] = Prompt{Name: p.Name, Description: p.Description, Arguments: args}
	}
	return out, nil
}

func decodePromptGet(raw []byte) (*PromptResult, error) {
	var w wirePromptGet
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	msgs := make([]PromptMessage, len(w.Messages))
	for i, m := range w.Messages {
		msgs[i] = PromptMessage{Role: m.Role, Content: m.Content}
	}
	return &PromptResult{Description: w.Description, Messages: msgs}, nil
}

func decodeResourceList(raw []byte) ([]ResourceInfo, error) {
	var w wireResourcesList
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]ResourceInfo, len(w.Resources))
	for i, r := range w.Resources {
		out[i] = ResourceInfo{URI: r.URI, Name: r.Name, Description: r.Description, MimeType: r.MimeType}
	}
	return out, nil
}

func decodeResourceRead(raw []byte) ([]ResourceContent, error) {
	var w wireResourceRead
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	out := make([]ResourceContent, len(w.Contents))
	for i, c := range w.Contents {
		out[i] = ResourceContent{URI: c.URI, MimeType: c.MimeType, Text: c.Text, Blob: c.Blob}
	}
	return out, nil
}
