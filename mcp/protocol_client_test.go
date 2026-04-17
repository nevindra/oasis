package mcp

import (
	"encoding/json"
	"testing"
)

func TestCallToolResult_Unmarshal(t *testing.T) {
	data := []byte(`{"content":[{"type":"text","text":"hi"}],"isError":false}`)
	var r CallToolResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Content) != 1 || r.Content[0].Text != "hi" {
		t.Errorf("got %+v", r)
	}
	if r.IsError {
		t.Errorf("IsError should be false")
	}
}

func TestInitializeResult_Unmarshal(t *testing.T) {
	data := []byte(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"test","version":"1.0"}}`)
	var r InitializeResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ServerInfo.Name != "test" || r.ProtocolVersion != "2024-11-05" {
		t.Errorf("got %+v", r)
	}
}
